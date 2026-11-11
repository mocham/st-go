"use strict";
// Rendering resources belong to this connection. Media tasks never join the
// ordered text-paint queue: a slow PDF/image must not delay terminal input/ACKs.
window.SceneClient = class {
  constructor(screen, viewport, send, focus) {
    this.screen = screen;
    this.viewport = viewport;
    this.send = send;
    this.focus = focus;
    this.surfaces = new Map();
    this.resources = new Map();
    this.previews = new Map();
    this.queries = new Map();
    this.closed = false;
    this.font = new FontFace("STTerminal", 'url("font.ttf")');
    this.fontReady = this.font
      .load()
      .then((font) => {
        if (!this.closed) document.fonts.add(font);
      })
      .catch(() => {});
  }
  color(n) {
    return "#" + (n >>> 0).toString(16).padStart(8, "0").slice(-6);
  }
  async apply(message) {
    await this.fontReady;
    if (this.closed) return;
    for (const update of message.scenes || []) {
      let s = this.surfaces.get(update.id);
      if (!s) {
        s = { canvas: document.createElement("canvas"), rows: new Map() };
        this.surfaces.set(update.id, s);
      }
      s.data = update;
      if (
        s.canvas.width !== update.width ||
        s.canvas.height !== update.height
      ) {
        s.canvas.width = update.width;
        s.canvas.height = update.height;
      }
      const u = update.update;
      if (u.reset) {
        s.rows.clear();
        const ctx = s.canvas.getContext("2d");
        ctx.fillStyle = this.color(u.background);
        ctx.fillRect(0, 0, s.canvas.width, s.canvas.height);
      }
      for (const row of u.rows || []) {
        s.rows.set(row.y, row);
        this.row(s, row);
      }
      for (const f of u.fills || []) {
        const ctx = s.canvas.getContext("2d");
        ctx.fillStyle = this.color(f.color);
        ctx.fillRect(f.x, f.y, f.width, f.height);
      }
      s.cursor = u.cursor;
      if (update.hidden) this.surfaces.delete(update.id);
    }
    this.composite();
    this.collect();
    this.screen.dataset.frames = String(
      Number(this.screen.dataset.frames || 0) + 1,
    );
    this.screen.dataset.textFrames = String(
      Number(this.screen.dataset.textFrames || 0) + 1,
    );
  }
  row(s, row) {
    const ctx = s.canvas.getContext("2d");
    ctx.save();
    ctx.beginPath();
    ctx.rect(row.x || 0, row.y, row.width || s.canvas.width, row.height);
    ctx.clip();
    ctx.fillStyle = this.color(row.background);
    ctx.fillRect(0, row.y, s.canvas.width, row.height);
    for (const run of row.text || []) {
      let x = run.x;
      const chars = run.cluster ? [run.text] : Array.from(run.text);
      ctx.fillStyle = this.color(run.bg);
      ctx.fillRect(x, row.y, chars.length * run.advance, row.height);
      ctx.font = `${run.style & 4 ? "italic " : ""}${run.style & 1 ? "bold " : ""}${row.fontSize || row.height}px STTerminal, monospace`;
      ctx.textBaseline = "alphabetic";
      ctx.fillStyle = this.color(run.fg);
      for (const char of chars) {
        ctx.save();
        ctx.beginPath();
        ctx.rect(x, row.y, run.advance, row.height);
        ctx.clip();
        ctx.fillText(char, x, row.y + row.baseline, run.advance);
        ctx.restore();
        x += run.advance;
      }
      if (run.style & 8)
        ctx.fillRect(
          run.x,
          row.y + row.baseline + 1,
          chars.length * run.advance,
          1,
        );
      if (run.style & 128)
        ctx.fillRect(
          run.x,
          row.y + Math.floor(row.height * 0.55),
          chars.length * run.advance,
          1,
        );
    }
    for (const m of row.media || []) {
      const key = `${m.id}:${m.page || 1}`,
        resource = this.resource(m.id);
      if (!resource.pages.has(key)) {
        resource.pages.set(key, null);
        this.bitmap(resource, m.page || 1, m.width, m.height)
          .then((bitmap) => {
            if (this.closed) return;
            resource.pages.set(key, bitmap);
            this.redrawResource(m.id);
          })
          .catch(() => {});
      }
      const bitmap = resource.pages.get(key);
      if (!bitmap) continue;
      let w = bitmap.width || bitmap.naturalWidth,
        h = bitmap.height || bitmap.naturalHeight;
      let scale =
        m.fit === "width"
          ? m.width / w
          : m.fit === "height"
            ? m.height / h
            : Math.min(m.width / w, m.height / h);
      if (m.fit === "native") scale = 1;
      w *= scale;
      h *= scale;
      ctx.save();
      ctx.beginPath();
      ctx.rect(m.clipX, m.clipY, m.clipWidth, m.clipHeight);
      ctx.clip();
      ctx.drawImage(
        bitmap,
        m.x + (m.width - w) / 2,
        m.y + (m.height - h) / 2,
        w,
        h,
      );
      ctx.restore();
    }
    ctx.restore();
  }
  composite() {
    if (this.closed) return;
    const ctx = this.screen.getContext("2d", { alpha: false });
    ctx.fillStyle = "#181818";
    ctx.fillRect(0, 0, this.screen.width, this.screen.height);
    const surfaces = Array.from(this.surfaces.values()).sort(
      (a, b) => Number(!!a.data.bar) - Number(!!b.data.bar),
    );
    for (const s of surfaces) {
      const d = s.data;
      if (d.hidden) continue;
      ctx.drawImage(s.canvas, d.x, d.y);
      const c = s.cursor;
      if (!c || !c.width || !c.height) continue;
      ctx.save();
      ctx.translate(d.x + c.x, d.y + c.y);
      ctx.fillStyle = this.color(c.color);
      ctx.strokeStyle = this.color(c.color);
      if (c.outline) {
        ctx.strokeRect(0.5, 0.5, c.width - 1, c.height - 1);
      } else if (c.shape === 3 || c.shape === 4) {
        ctx.fillRect(0, c.height - c.thickness, c.width, c.thickness);
      } else if (c.shape === 5 || c.shape === 6) {
        ctx.fillRect(0, 0, c.thickness, c.height);
      } else if (c.text) {
        ctx.fillRect(0, 0, c.width, c.height);
        ctx.beginPath();
        ctx.rect(0, 0, c.width, c.height);
        ctx.clip();
        ctx.fillStyle = this.color(c.fg);
        ctx.font = `${c.height}px STTerminal, monospace`;
        ctx.textBaseline = "alphabetic";
        ctx.fillText(c.text, 0, c.baseline, c.width);
      }
      ctx.restore();
    }
    if (
      !this.animationTimer &&
      Array.from(this.resources.values()).some((r) => r.animated)
    ) {
      this.animationTimer = setTimeout(() => {
        this.animationTimer = 0;
        if (this.closed) return;
        let animated = false;
        for (const s of this.surfaces.values())
          for (const row of s.rows.values())
            if (
              (row.media || []).some((m) => this.resources.get(m.id)?.animated)
            ) {
              this.row(s, row);
              animated = true;
            }
        if (animated) this.composite();
      }, 33);
    }
  }
  redrawResource(id) {
    for (const s of this.surfaces.values())
      for (const row of s.rows.values())
        if ((row.media || []).some((m) => m.id === id)) this.row(s, row);
    this.composite();
  }
  resource(id) {
    let r = this.resources.get(id);
    if (r) return r;
    r = { id, pages: new Map(), abort: new AbortController() };
    this.resources.set(id, r);
    r.ready = fetch(`media/${id}`, {
      method: "HEAD",
      signal: r.abort.signal,
    }).then(async (response) => {
      if (!response.ok) throw new Error(await response.text());
      if (response.headers.get("Content-Type").includes("pdf")) {
        const pdfjs = await import("./pdfjs/build/pdf.mjs");
        pdfjs.GlobalWorkerOptions.workerSrc = new URL(
          "pdfjs/build/pdf.worker.mjs",
          location.href,
        ).href;
        r.pdfjs = pdfjs;
        r.loading = pdfjs.getDocument({
          url: new URL(`media/${id}`, location.href).href,
          disableAutoFetch: true,
          disableStream: true,
          rangeChunkSize: 65536,
          cMapUrl: new URL("pdfjs/cmaps/", location.href).href,
          cMapPacked: true,
          standardFontDataUrl: new URL("pdfjs/standard_fonts/", location.href)
            .href,
          wasmUrl: new URL("pdfjs/wasm/", location.href).href,
        });
        r.pdf = await r.loading.promise;
        if (r.abort.signal.aborted) {
          r.loading.destroy();
          throw new Error("Cancelled");
        }
      } else {
        const image = new Image();
        image.src = `media/${id}`;
        await image.decode();
        r.image = image;
        r.animated = /gif|webp/.test(response.headers.get("Content-Type"));
      }
      return r;
    });
    r.ready.catch(() => {});
    return r;
  }
  async bitmap(resource, page, width, height) {
    await resource.ready;
    if (resource.image) return resource.image;
    const p = await resource.pdf.getPage(
      Math.max(1, Math.min(page, resource.pdf.numPages)),
    );
    const base = p.getViewport({ scale: 1 });
    let scale = Math.min(
      width / base.width,
      height / base.height,
      Math.sqrt((4 * 1024 * 1024) / (base.width * base.height)),
    );
    const viewport = p.getViewport({ scale }),
      canvas = document.createElement("canvas");
    canvas.width = Math.ceil(viewport.width);
    canvas.height = Math.ceil(viewport.height);
    await p.render({ canvasContext: canvas.getContext("2d"), viewport })
      .promise;
    return canvas;
  }
  documents(queries, cancelled) {
    for (const id of cancelled || []) {
      const query = this.queries.get(id);
      if (query) query.cancelled = true;
    }
    for (const request of queries || []) {
      const token = { cancelled: false };
      this.queries.set(request.id, token);
      const r = this.resource(request.media);
      r.users = (r.users || 0) + 1;
      this.document(r, request.query, token)
        .then((data) => {
          if (!this.closed && !token.cancelled)
            this.send({ type: "document_reply", id: request.id, data });
        })
        .catch((error) => {
          if (!this.closed && !token.cancelled)
            this.send({
              type: "document_reply",
              id: request.id,
              data: { error: error.message },
            });
        })
        .finally(() => {
          this.queries.delete(request.id);
          r.users--;
          this.collect();
        });
    }
  }
  async document(r, q, token) {
    await r.ready;
    if (!r.pdf) throw new Error("Not a PDF document");
    if (q.kind === "text") {
      const page = await r.pdf.getPage(
          Math.min(r.pdf.numPages, Math.max(1, q.page || 1)),
        ),
        text = await page.getTextContent();
      let result = "";
      for (const item of text.items) {
        result += item.str + (item.hasEOL ? "\n" : " ");
        if (result.length >= 65536) break;
      }
      return {
        text: result.slice(0, 65536),
        truncated: result.length >= 65536,
      };
    }
    if (q.kind === "toc") {
      const toc = [];
      let truncated = false;
      const visit = async (items, level) => {
        for (const item of items || []) {
          if (token?.cancelled) throw new Error("Cancelled");
          if (toc.length >= 4096) {
            truncated = true;
            return;
          }
          let page = 0;
          try {
            const dest =
              typeof item.dest === "string"
                ? await r.pdf.getDestination(item.dest)
                : item.dest;
            if (dest)
              page =
                1 +
                (typeof dest[0] === "number"
                  ? dest[0]
                  : await r.pdf.getPageIndex(dest[0]));
          } catch (_) {}
          toc.push({ title: item.title.slice(0, 128), page, level });
          await visit(item.items, level + 1);
        }
      };
      await visit(await r.pdf.getOutline(), 0);
      return { toc, truncated };
    }
    if (q.kind === "search") {
      const pages = [],
        needle = q.text.toLocaleLowerCase();
      if (!needle) return { pages };
      for (let n = 1; n <= Math.min(r.pdf.numPages, 4096); n++) {
        if (this.closed || r.abort.signal.aborted || token?.cancelled)
          throw new Error("Cancelled");
        const page = await r.pdf.getPage(n),
          text = await page.getTextContent();
        const haystack = text.items
          .map((item) => item.str)
          .join(" ")
          .toLocaleLowerCase();
        for (
          let at = haystack.indexOf(needle);
          at >= 0;
          at = haystack.indexOf(needle, at + needle.length)
        ) {
          pages.push(n);
          if (pages.length >= 4096) return { pages, truncated: true };
        }
      }
      return { pages, truncated: r.pdf.numPages > 4096 };
    }
    throw new Error("Unsupported document query");
  }
  previewsApply(message) {
    const live = new Set();
    for (const overlay of message.media || []) {
      live.add(overlay.id);
      let p = this.previews.get(overlay.id);
      if (!p) {
        const element = document.createElement("div");
        element.className = "media-preview";
        element.tabIndex = 0;
        this.viewport.appendChild(element);
        p = { element, serial: 0 };
        this.previews.set(overlay.id, p);
        element.addEventListener("keydown", (e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            this.focus();
          } else if (e.key === "Tab" && p.data.view.captureTab) {
            e.preventDefault();
            this.focus();
            this.send({ type: "key", key: "Tab", modifiers: (e.shiftKey ? 1 : 0) | (e.ctrlKey ? 4 : 0) | (e.altKey ? 8 : 0) | (e.metaKey ? 64 : 0) });
          }
        });
        element.addEventListener("scroll", () => {
          clearTimeout(p.scrollTimer);
          p.scrollTimer = setTimeout(() => this.previewStatus(p), 100);
        });
      }
      const old = p.data;
      p.data = overlay;
      this.place(p);
      if (
        !old ||
        old.view.id !== overlay.view.id ||
        old.width !== overlay.width ||
        old.height !== overlay.height ||
        (!overlay.view.continuous && old.view.page !== overlay.view.page) ||
        old.view.zoom !== overlay.view.zoom ||
        old.view.fit !== overlay.view.fit
      ) {
        this.loadPreview(p).catch((error) => {
          if (!this.closed && p.element.isConnected && p.data === overlay) {
            p.element.dataset.error = error.message;
            this.previewStatus(p, error.message);
          }
        });
      } else this.scrollPreview(p);
    }
    for (const [id, p] of this.previews)
      if (!live.has(id)) {
        this.stopPreview(p);
        p.element.remove();
        this.previews.delete(id);
      }
    this.collect();
  }
  place(p) {
    const d = p.data,
      ratio = window.devicePixelRatio || 1;
    Object.assign(p.element.style, {
      left: d.x / ratio + "px",
      top: d.y / ratio + "px",
      width: d.width / ratio + "px",
      height: d.height / ratio + "px",
      background: this.color(d.view.background || 0xfff7f5ef),
    });
  }
  async loadPreview(p) {
    this.stopPreview(p);
    p.loading = true;
    const serial = p.serial,
      d = p.data,
      r = this.resource(d.view.id),
      ratio = window.devicePixelRatio || 1;
    await r.ready;
    if (this.closed || serial !== p.serial) return;
    const content = document.createElement("div");
    content.className = "media-content";
    if (r.image) {
      const image = r.image.cloneNode();
      image.style.display = "block";
      image.style.maxWidth = "none";
      const w = r.image.naturalWidth,
        h = r.image.naturalHeight;
      const scale =
        d.view.fit === "native"
          ? 1
          : d.view.fit === "height"
            ? d.height / h
            : d.view.fit === "contain"
              ? Math.min(d.width / w, d.height / h)
              : d.width / w;
      image.style.width =
        (w * scale * (d.view.zoom || 100)) / 100 / ratio + "px";
      content.appendChild(image);
    } else {
      const pages = d.view.continuous
        ? Array.from(
            { length: Math.min(r.pdf.numPages, 4096) },
            (_, i) => i + 1,
          )
        : [Math.max(1, Math.min(d.view.page || 1, r.pdf.numPages))];
      // Reserve geometry using the first page, then lazily render only visible
      // pages. The PDF worker owns parsing and range fetches.
      const first = await r.pdf.getPage(pages[0]);
      if (this.closed || serial !== p.serial) return;
      const base = first.getViewport({ scale: 1 });
      let width = (d.width * (d.view.zoom || 100)) / 100;
      if (d.view.fit === "height")
        width = (d.height * base.width) / base.height;
      if (d.view.fit === "contain")
        width = Math.min(width, (d.height * base.width) / base.height);
      if (d.view.fit === "native") width = base.width;
      for (const n of pages) {
        const box = document.createElement("div");
        box.className = "pdf-page";
        box.dataset.page = n;
        box.style.width = width / ratio + "px";
        box.style.height = (base.height * width) / base.width / ratio + "px";
        content.appendChild(box);
      }
      p.tasks = new Map();
      p.queue = [];
      p.active = 0;
      const pump = () => {
        if (this.closed || serial !== p.serial) return;
        while (p.active < 2 && p.queue.length) {
          const box = p.queue.shift();
          if (!box.near || box.dataset.rendered) continue;
          box.dataset.rendered = "1";
          p.active++;
          this.renderPage(p, r, box, width, ratio, serial)
            .catch((error) => {
              if (error.name !== "RenderingCancelledException")
                box.dataset.error = error.message;
            })
            .finally(() => {
              if (serial === p.serial) {
                p.active--;
                pump();
              }
            });
        }
      };
      p.observer = new IntersectionObserver(
        (entries) => {
          for (const entry of entries) {
            const box = entry.target;
            box.near = entry.isIntersecting;
            if (box.near) {
              if (!box.dataset.rendered && !p.queue.includes(box))
                p.queue.push(box);
            } else {
              p.tasks.get(box)?.cancel();
              box.replaceChildren();
              delete box.dataset.rendered;
            }
          }
          pump();
        },
        { root: p.element, rootMargin: "100% 0px" },
      );
      for (const box of content.children) p.observer.observe(box);
    }
    if (this.closed || serial !== p.serial) return;
    const scroll = p.element.scrollTop;
    p.element.replaceChildren(content);
    p.element.scrollTop = scroll;
    p.positionID = -1;
    p.scroll = 0;
    p.loading = false;
    this.scrollPreview(p);
    this.previewStatus(p);
  }
  async renderPage(p, r, box, width, ratio, serial) {
    const page = await r.pdf.getPage(Number(box.dataset.page));
    if (this.closed || serial !== p.serial || !box.near) return;
    const base = page.getViewport({ scale: 1 });
    const scale = Math.min(
      width / base.width,
      Math.sqrt((8 * 1024 * 1024) / (base.width * base.height)),
    );
    const v = page.getViewport({ scale }),
      css = page.getViewport({ scale: width / base.width / ratio });
    const canvas = document.createElement("canvas");
    canvas.width = Math.ceil(v.width);
    canvas.height = Math.ceil(v.height);
    canvas.style.width = css.width + "px";
    canvas.style.height = css.height + "px";
    const task = page.render({
      canvasContext: canvas.getContext("2d"),
      viewport: v,
    });
    p.tasks.set(box, task);
    try {
      await task.promise;
    } finally {
      if (p.tasks.get(box) === task) p.tasks.delete(box);
    }
    if (this.closed || serial !== p.serial || !box.near) return;
    box.style.height = css.height + "px";
    box.appendChild(canvas);
    const text = await page.getTextContent();
    if (this.closed || serial !== p.serial || !box.near) return;
    const layer = document.createElement("div");
    layer.className = "textLayer";
    layer.style.setProperty("--total-scale-factor", String(css.scale));
    box.appendChild(layer);
    await new r.pdfjs.TextLayer({
      textContentSource: text,
      container: layer,
      viewport: css,
    }).render();
    this.highlight(p);
  }
  stopPreview(p) {
    p.serial++;
    p.observer?.disconnect();
    for (const task of p.tasks?.values() || []) task.cancel();
    p.queue = [];
    clearTimeout(p.scrollTimer);
  }
  highlight(p) {
    const text = (p.data.view.search || "").toLocaleLowerCase();
    for (const span of p.element.querySelectorAll(".textLayer span"))
      span.classList.toggle(
        "search-hit",
        !!text && span.textContent.toLocaleLowerCase().includes(text),
      );
  }
  scrollPreview(p) {
    const v = p.data.view;
    if (p.positionID !== v.positionID) {
      const page = p.element.querySelector(`[data-page="${v.page}"]`);
      p.element.scrollTop =
        page && v.position === 0
          ? page.offsetTop
          : ((p.element.scrollHeight - p.element.clientHeight) * v.position) /
            1000000;
      p.positionID = v.positionID;
      p.scroll = 0;
    }
    p.element.scrollTop +=
      ((v.scroll - p.scroll) * 28) / (window.devicePixelRatio || 1);
    p.scroll = v.scroll;
    this.highlight(p);
  }
  previewStatus(p, error = "") {
    if (this.closed || !p.element.isConnected) return;
    if (p.loading && !error) return;
    const v = p.data.view,
      r = this.resources.get(v.id);
    let page = v.page || 1;
    for (const box of p.element.querySelectorAll(".pdf-page")) {
      if (box.offsetTop > p.element.scrollTop + 1) break;
      page = Number(box.dataset.page);
    }
    this.send({
      type: "media_status",
      mediaStatus: {
        id: v.id,
        generation: v.generation,
        positionID: v.positionID,
        page,
        pages: r?.pdf?.numPages || 1,
        position: Math.max(
          0,
          Math.min(
            1000000,
            Math.round(
              (p.element.scrollTop /
                Math.max(1, p.element.scrollHeight - p.element.clientHeight)) *
                1000000,
            ),
          ),
        ),
        width: r?.image?.naturalWidth || 0,
        height: r?.image?.naturalHeight || 0,
        error: error.slice(0, 4096),
      },
    });
  }
  collect() {
    const used = new Set();
    for (const s of this.surfaces.values())
      for (const row of s.rows.values())
        for (const m of row.media || []) used.add(m.id);
    for (const p of this.previews.values()) used.add(p.data.view.id);
    for (const [id, r] of this.resources)
      if (!used.has(id) && !r.users) {
        r.abort.abort();
        r.loading?.destroy();
        r.pages.clear();
        this.resources.delete(id);
      }
  }
  close() {
    this.closed = true;
    clearTimeout(this.animationTimer);
    for (const p of this.previews.values()) {
      this.stopPreview(p);
      p.element.remove();
    }
    for (const r of this.resources.values()) {
      r.abort.abort();
      r.loading?.destroy();
    }
    this.surfaces.clear();
    this.previews.clear();
    this.resources.clear();
    document.fonts.delete(this.font);
  }
};
