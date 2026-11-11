#include "djvu_bridge.h"

#include <libdjvu/ddjvuapi.h>
#include <libdjvu/miniexp.h>

#include <ctype.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

struct zat_djvu_document {
  ddjvu_context_t* context;
  ddjvu_document_t* document;
  ddjvu_format_t* format;
  int pages;
  char error[512];
};

typedef struct {
  int page;
  int level;
  char* title;
  size_t title_length;
} zat_djvu_outline_entry;

struct zat_djvu_outline {
  zat_djvu_outline_entry* entries;
  size_t count;
  size_t capacity;
  size_t bytes;
  size_t max_bytes;
  int truncated;
};

static void set_error(zat_djvu_document* document, const char* message) {
  if (document == NULL) {
    return;
  }
  if (message == NULL || *message == '\0') {
    message = "DjVuLibre operation failed";
  }
  snprintf(document->error, sizeof(document->error), "%s", message);
}

/* DjVuLibre is asynchronous even for local files. As in zathura-djvu, every
 * synchronous query drains the context queue and waits for the relevant job. */
static int handle_messages(zat_djvu_document* document, int wait) {
  if (document == NULL || document->context == NULL) {
    return 0;
  }
  if (wait) {
    if (ddjvu_message_wait(document->context) == NULL) {
      set_error(document, "DjVuLibre message queue stopped unexpectedly");
      return 0;
    }
  }
  const ddjvu_message_t* message;
  int ok = 1;
  while ((message = ddjvu_message_peek(document->context)) != NULL) {
    if (message->m_any.tag == DDJVU_ERROR) {
      set_error(document, message->m_error.message);
      ok = 0;
    }
    ddjvu_message_pop(document->context);
  }
  return ok;
}

static int wait_for_document(zat_djvu_document* document) {
  int ready = 0;
  while (!ready) {
    const ddjvu_message_t* message = ddjvu_message_wait(document->context);
    if (message == NULL) {
      set_error(document, "DjVuLibre stopped before document information was available");
      return 0;
    }
    while ((message = ddjvu_message_peek(document->context)) != NULL) {
      if (message->m_any.tag == DDJVU_ERROR) {
        set_error(document, message->m_error.message);
        ddjvu_message_pop(document->context);
        return 0;
      }
      if (message->m_any.tag == DDJVU_DOCINFO && message->m_any.document == document->document) {
        ready = 1;
      }
      ddjvu_message_pop(document->context);
    }
    if (ddjvu_document_decoding_error(document->document)) {
      set_error(document, "DjVu document decoding failed");
      return 0;
    }
  }
  return 1;
}

zat_djvu_document* zat_djvu_open(const char* path, uint32_t background) {
  if (path == NULL) {
    return NULL;
  }
  zat_djvu_document* result = calloc(1, sizeof(*result));
  if (result == NULL) {
    return NULL;
  }

  /* RGBMASK32 is the format used by zathura-djvu for its Cairo target. Pick
   * masks for an in-memory R,G,B,A byte sequence and XOR the alpha byte opaque. */
  const uint16_t endian = 1;
  unsigned int masks[4];
  if (*(const unsigned char*)&endian != 0) {
    masks[0] = 0x000000ffU;
    masks[1] = 0x0000ff00U;
    masks[2] = 0x00ff0000U;
    masks[3] = 0xff000000U;
  } else {
    masks[0] = 0xff000000U;
    masks[1] = 0x00ff0000U;
    masks[2] = 0x0000ff00U;
    masks[3] = 0x000000ffU;
  }
  result->format = ddjvu_format_create(DDJVU_FORMAT_RGBMASK32, 4, masks);
  if (result->format == NULL) {
    set_error(result, "could not create the DjVu pixel format");
    return result;
  }
  ddjvu_format_set_row_order(result->format, 1);
  /* ddjvu_format_set_white takes B,G,R even though callers use #RRGGBB. */
  ddjvu_format_set_white(result->format, background & 0xff,
                         (background >> 8) & 0xff, (background >> 16) & 0xff);

  result->context = ddjvu_context_create("st-go-zat-djvu");
  if (result->context == NULL) {
    set_error(result, "could not create a DjVuLibre context");
    return result;
  }
  result->document = ddjvu_document_create_by_filename_utf8(result->context, path, 0);
  if (result->document == NULL) {
    set_error(result, "could not open the DjVu document");
    return result;
  }
  if (!wait_for_document(result)) {
    return result;
  }
  result->pages = ddjvu_document_get_pagenum(result->document);
  if (result->pages < 1) {
    set_error(result, "DjVu document has no pages");
  }
  return result;
}

void zat_djvu_close(zat_djvu_document* document) {
  if (document == NULL) {
    return;
  }
  if (document->document != NULL) {
    ddjvu_document_release(document->document);
  }
  if (document->format != NULL) {
    ddjvu_format_release(document->format);
  }
  if (document->context != NULL) {
    ddjvu_context_release(document->context);
  }
  free(document);
}

const char* zat_djvu_error(const zat_djvu_document* document) {
  return document != NULL ? document->error : "could not allocate a DjVu document";
}

int zat_djvu_pages(const zat_djvu_document* document) {
  return document != NULL ? document->pages : 0;
}

int zat_djvu_render_page(zat_djvu_document* document, int page_number, uint64_t max_pixels,
                         unsigned char** pixels, int* width, int* height) {
  if (document == NULL || document->document == NULL || document->format == NULL ||
      pixels == NULL || width == NULL || height == NULL || page_number < 0 ||
      page_number >= document->pages || max_pixels == 0) {
    set_error(document, "invalid DjVu page render request");
    return 0;
  }
  *pixels = NULL;
  ddjvu_page_t* page = ddjvu_page_create_by_pageno(document->document, page_number);
  if (page == NULL) {
    set_error(document, "could not create the DjVu page decoder");
    return 0;
  }
  while (!ddjvu_page_decoding_done(page)) {
    if (!handle_messages(document, 1)) {
      ddjvu_page_release(page);
      return 0;
    }
  }
  if (!handle_messages(document, 0) || ddjvu_page_decoding_error(page)) {
    set_error(document, "DjVu page decoding failed");
    ddjvu_page_release(page);
    return 0;
  }

  int output_width = ddjvu_page_get_width(page);
  int output_height = ddjvu_page_get_height(page);
  if (output_width < 1 || output_height < 1) {
    set_error(document, "DjVu page has invalid dimensions");
    ddjvu_page_release(page);
    return 0;
  }
  uint64_t count = (uint64_t)output_width * (uint64_t)output_height;
  if (count > max_pixels) {
    const double scale = sqrt((double)max_pixels / (double)count);
    output_width = (int)((double)output_width * scale);
    output_height = (int)((double)output_height * scale);
    if (output_width < 1) output_width = 1;
    if (output_height < 1) output_height = 1;
    count = (uint64_t)output_width * (uint64_t)output_height;
  }
  if (count > SIZE_MAX / 4) {
    set_error(document, "DjVu page is too large");
    ddjvu_page_release(page);
    return 0;
  }
  unsigned char* output = malloc((size_t)count * 4);
  if (output == NULL) {
    set_error(document, "could not allocate the DjVu page raster");
    ddjvu_page_release(page);
    return 0;
  }
  ddjvu_rect_t page_rect = {0, 0, (unsigned int)output_width, (unsigned int)output_height};
  ddjvu_rect_t render_rect = page_rect;
  const int rendered = ddjvu_page_render(page, DDJVU_RENDER_COLOR, &page_rect, &render_rect,
                                         document->format, (unsigned long)output_width * 4,
                                         (char*)output);
  ddjvu_page_release(page);
  if (!rendered) {
    free(output);
    set_error(document, "DjVuLibre could not render the page");
    return 0;
  }
  *pixels = output;
  *width = output_width;
  *height = output_height;
  return 1;
}

void zat_djvu_free(void* pointer) {
  free(pointer);
}

typedef struct {
  char* output;
  size_t capacity;
  size_t length;
  size_t nodes;
  int separator;
  int truncated;
} text_collector;

static void collect_bytes(text_collector* collector, const char* text, size_t length) {
  if (collector->truncated || length == 0) {
    return;
  }
  const size_t available = collector->capacity - 1 - collector->length;
  if (length > available) {
    length = available;
    collector->truncated = 1;
  }
  if (length > 0) {
    memcpy(collector->output + collector->length, text, length);
    collector->length += length;
  }
}

static void collect_zone(text_collector* collector, miniexp_t zone, int depth) {
  if (collector->truncated || depth > 64 || ++collector->nodes > 1048576 ||
      !miniexp_consp(zone) || !miniexp_symbolp(miniexp_car(zone))) {
    if (depth > 64 || collector->nodes > 1048576) collector->truncated = 1;
    return;
  }
  const char* type = miniexp_to_name(miniexp_car(zone));
  miniexp_t inner = miniexp_cdr(zone);
  for (int index = 0; index < 4 && miniexp_consp(inner); ++index) {
    inner = miniexp_cdr(inner);
  }
  while (miniexp_consp(inner) && !collector->truncated) {
    const miniexp_t child = miniexp_car(inner);
    if (miniexp_stringp(child)) {
      const char* text = NULL;
      const size_t length = miniexp_to_lstr(child, &text);
      if (collector->length > 0 && collector->separator != 0) {
        const char separator = (char)collector->separator;
        collect_bytes(collector, &separator, 1);
      }
      collector->separator = 0;
      if (text != NULL) collect_bytes(collector, text, length);
    } else {
      collect_zone(collector, child, depth + 1);
    }
    inner = miniexp_cdr(inner);
  }
  if (type != NULL && strcmp(type, "word") == 0) {
    if (collector->separator == 0) collector->separator = ' ';
  } else if (type == NULL || strcmp(type, "char") != 0) {
    collector->separator = '\n';
  }
}

int zat_djvu_page_text(zat_djvu_document* document, int page, char* output,
                       size_t capacity, size_t* length, int* truncated) {
  if (document == NULL || document->document == NULL || output == NULL || capacity < 2 ||
      length == NULL || truncated == NULL || page < 0 || page >= document->pages) {
    set_error(document, "invalid DjVu text request");
    return 0;
  }
  miniexp_t expression;
  while ((expression = ddjvu_document_get_pagetext(document->document, page, "char")) == miniexp_dummy) {
    if (!handle_messages(document, 1)) return 0;
  }
  if (!handle_messages(document, 0)) {
    if (expression != miniexp_nil) ddjvu_miniexp_release(document->document, expression);
    return 0;
  }
  text_collector collector = {output, capacity, 0, 0, 0, 0};
  if (expression != miniexp_nil && !miniexp_symbolp(expression)) {
    collect_zone(&collector, expression, 0);
    ddjvu_miniexp_release(document->document, expression);
  } else if (miniexp_symbolp(expression)) {
    set_error(document, "DjVu hidden text is unavailable");
    ddjvu_miniexp_release(document->document, expression);
    return 0;
  }
  output[collector.length] = '\0';
  *length = collector.length;
  *truncated = collector.truncated;
  return 1;
}

static int outline_page(zat_djvu_document* document, const char* link, int* cursor) {
  if (link == NULL || link[0] != '#') {
    return 0;
  }
  int numeric = link[1] != '\0';
  for (const char* character = link + 1; *character != '\0'; ++character) {
    if (!isdigit((unsigned char)*character)) {
      numeric = 0;
      break;
    }
  }
  if (numeric) {
    const long page = strtol(link + 1, NULL, 10);
    return page > 0 && page <= document->pages ? (int)page : 0;
  }
  const int files = ddjvu_document_get_filenum(document->document);
  for (int index = 0; index < files; ++index) {
    const int file = (*cursor + index) % files;
    ddjvu_fileinfo_t info = {0};
    ddjvu_status_t status;
    while ((status = ddjvu_document_get_fileinfo(document->document, file, &info)) < DDJVU_JOB_OK) {
      if (!handle_messages(document, 1)) return 0;
    }
    if (status == DDJVU_JOB_OK &&
        info.id != NULL && strcmp(link + 1, info.id) == 0 && info.pageno >= 0) {
      *cursor = (file + 1) % files;
      return info.pageno + 1;
    }
  }
  return 0;
}

static void collect_outline(zat_djvu_document* document, zat_djvu_outline* outline,
                            miniexp_t expression, int level, int* cursor) {
  if (level > 31) {
    outline->truncated = 1;
    return;
  }
  while (miniexp_consp(expression)) {
    const miniexp_t item = miniexp_car(expression);
    if (miniexp_consp(item) && miniexp_stringp(miniexp_car(item)) &&
        miniexp_stringp(miniexp_cadr(item))) {
      const char* title = NULL;
      const char* link = miniexp_to_str(miniexp_cadr(item));
      const size_t title_length = miniexp_to_lstr(miniexp_car(item), &title);
      const int page = outline_page(document, link, cursor);
      if (page > 0 && title != NULL) {
        if (outline->count == outline->capacity || title_length > outline->max_bytes - outline->bytes) {
          outline->truncated = 1;
          return;
        }
        char* copy = malloc(title_length + 1);
        if (copy == NULL) {
          outline->truncated = 1;
          return;
        }
        memcpy(copy, title, title_length);
        copy[title_length] = '\0';
        zat_djvu_outline_entry* entry = &outline->entries[outline->count++];
        entry->page = page;
        entry->level = level;
        entry->title = copy;
        entry->title_length = title_length;
        outline->bytes += title_length;
        collect_outline(document, outline, miniexp_cddr(item), level + 1, cursor);
        if (outline->truncated) return;
      }
    }
    expression = miniexp_cdr(expression);
  }
}

zat_djvu_outline* zat_djvu_get_outline(zat_djvu_document* document,
                                       size_t max_entries, size_t max_bytes) {
  if (document == NULL || document->document == NULL || max_entries == 0 || max_bytes == 0) {
    set_error(document, "invalid DjVu outline request");
    return NULL;
  }
  miniexp_t expression;
  while ((expression = ddjvu_document_get_outline(document->document)) == miniexp_dummy) {
    if (!handle_messages(document, 1)) return NULL;
  }
  if (!handle_messages(document, 0)) {
    if (expression != miniexp_nil) ddjvu_miniexp_release(document->document, expression);
    return NULL;
  }
  zat_djvu_outline* result = calloc(1, sizeof(*result));
  if (result == NULL) {
    if (expression != miniexp_nil) ddjvu_miniexp_release(document->document, expression);
    set_error(document, "could not allocate the DjVu outline");
    return NULL;
  }
  result->entries = calloc(max_entries, sizeof(*result->entries));
  result->capacity = max_entries;
  result->max_bytes = max_bytes;
  if (result->entries == NULL) {
    zat_djvu_outline_free(result);
    if (expression != miniexp_nil) ddjvu_miniexp_release(document->document, expression);
    set_error(document, "could not allocate the DjVu outline entries");
    return NULL;
  }
  if (miniexp_consp(expression) && miniexp_car(expression) == miniexp_symbol("bookmarks")) {
    int cursor = 0;
    collect_outline(document, result, miniexp_cdr(expression), 0, &cursor);
  } else if (expression != miniexp_nil && miniexp_symbolp(expression)) {
    set_error(document, "DjVu outline is unavailable");
    zat_djvu_outline_free(result);
    ddjvu_miniexp_release(document->document, expression);
    return NULL;
  }
  if (expression != miniexp_nil) ddjvu_miniexp_release(document->document, expression);
  return result;
}

size_t zat_djvu_outline_count(const zat_djvu_outline* outline) {
  return outline != NULL ? outline->count : 0;
}
int zat_djvu_outline_truncated(const zat_djvu_outline* outline) {
  return outline != NULL ? outline->truncated : 0;
}
int zat_djvu_outline_page(const zat_djvu_outline* outline, size_t index) {
  return outline != NULL && index < outline->count ? outline->entries[index].page : 0;
}
int zat_djvu_outline_level(const zat_djvu_outline* outline, size_t index) {
  return outline != NULL && index < outline->count ? outline->entries[index].level : 0;
}
const char* zat_djvu_outline_title(const zat_djvu_outline* outline, size_t index) {
  return outline != NULL && index < outline->count ? outline->entries[index].title : NULL;
}
size_t zat_djvu_outline_title_length(const zat_djvu_outline* outline, size_t index) {
  return outline != NULL && index < outline->count ? outline->entries[index].title_length : 0;
}
void zat_djvu_outline_free(zat_djvu_outline* outline) {
  if (outline == NULL) return;
  for (size_t index = 0; index < outline->count; ++index) {
    free(outline->entries[index].title);
  }
  free(outline->entries);
  free(outline);
}
