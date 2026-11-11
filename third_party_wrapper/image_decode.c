/* Bounded fast-path decoders for full terminal builds.
 *
 * Go validates the encoded input and dimensions before calling these helpers,
 * allocates the exact output buffer, and falls back to its native decoders on
 * any error. No decoder state or Go pointer survives either call.
 */
#include <setjmp.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>

#include <jpeglib.h>
#include <png.h>

static int
valid_output(size_t output_len, int width, int height)
{
	if (width <= 0 || height <= 0 || (size_t)width > SIZE_MAX / 4)
		return 0;
	size_t stride = (size_t)width * 4;
	return (size_t)height <= SIZE_MAX / stride &&
	       output_len == stride * (size_t)height;
}

int
st_go_decode_png_rgba(const unsigned char *input, size_t input_len,
		      unsigned char *output, size_t output_len,
		      int expected_width, int expected_height)
{
	if (!input || !output || input_len == 0 ||
	    !valid_output(output_len, expected_width, expected_height))
		return 0;

	png_image image = {0};
	image.version = PNG_IMAGE_VERSION;
	if (!png_image_begin_read_from_memory(&image, input, input_len))
		return 0;
	if (image.width != (png_uint_32)expected_width ||
	    image.height != (png_uint_32)expected_height) {
		png_image_free(&image);
		return 0;
	}
	image.format = PNG_FORMAT_RGBA;
	if (PNG_IMAGE_SIZE(image) != output_len ||
	    !png_image_finish_read(&image, NULL, output, 0, NULL)) {
		png_image_free(&image);
		return 0;
	}
	png_image_free(&image);
	return 1;
}

struct st_go_jpeg_error {
	struct jpeg_error_mgr manager;
	jmp_buf jump;
};

static void
st_go_jpeg_fail(j_common_ptr decoder)
{
	struct st_go_jpeg_error *error = (struct st_go_jpeg_error *)decoder->err;
	longjmp(error->jump, 1);
}

int
st_go_decode_jpeg_rgba(const unsigned char *input, size_t input_len,
		       unsigned char *output, size_t output_len,
		       int expected_width, int expected_height)
{
	if (!input || !output || input_len == 0 ||
	    !valid_output(output_len, expected_width, expected_height))
		return 0;

	struct jpeg_decompress_struct decoder = {0};
	struct st_go_jpeg_error error;
	decoder.err = jpeg_std_error(&error.manager);
	error.manager.error_exit = st_go_jpeg_fail;
	if (setjmp(error.jump)) {
		jpeg_destroy_decompress(&decoder);
		return 0;
	}

	jpeg_create_decompress(&decoder);
	jpeg_mem_src(&decoder, input, input_len);
	if (jpeg_read_header(&decoder, TRUE) != JPEG_HEADER_OK ||
	    decoder.image_width != (JDIMENSION)expected_width ||
	    decoder.image_height != (JDIMENSION)expected_height) {
		jpeg_destroy_decompress(&decoder);
		return 0;
	}
	decoder.out_color_space = JCS_EXT_RGBA;
	if (!jpeg_start_decompress(&decoder) || decoder.output_components != 4 ||
	    decoder.output_width != (JDIMENSION)expected_width ||
	    decoder.output_height != (JDIMENSION)expected_height) {
		jpeg_destroy_decompress(&decoder);
		return 0;
	}

	size_t stride = (size_t)expected_width * 4;
	while (decoder.output_scanline < decoder.output_height) {
		JSAMPROW row = output + (size_t)decoder.output_scanline * stride;
		if (jpeg_read_scanlines(&decoder, &row, 1) != 1) {
			jpeg_destroy_decompress(&decoder);
			return 0;
		}
	}
	if (!jpeg_finish_decompress(&decoder)) {
		jpeg_destroy_decompress(&decoder);
		return 0;
	}
	jpeg_destroy_decompress(&decoder);
	return 1;
}
