(() => {
  "use strict";

  // The read-only explorer: renders Markdown with Slate, and opens files from
  // the tree without reloading the page, so the tree keeps its open folders.
  // Without JavaScript (or if anything here fails) links load whole pages.
  const main = document.getElementById("main");
  const host = document.getElementById("document-viewer");
  const tree = document.querySelector(".workspace nav");
  if (!main || !host) return;

  // Slate supports one editor per page: mount it once, then swap its text.
  let editor = null;

  // Pictures: a relative image is a file of the space, which path?raw serves
  // (owned spaces and the docs; see data-raw-images), as Slate Viewer shows it.
  function imageUrl(url) {
    if (main.dataset.rawImages !== "true" || /^[a-z][a-z0-9+.-]*:|^\/\/|^#/i.test(url)) return null;
    try {
      const resolved = new URL(url, window.location.href);
      if (resolved.origin !== window.location.origin) return null;
      resolved.search = "raw";
      resolved.hash = "";
      return resolved.href;
    } catch (error) {
      return null;
    }
  }
  function source() {
    return document.getElementById("document-source");
  }
  function showSource() {
    host.hidden = true;
    source().hidden = false;
  }
  async function render() {
    const pre = source();
    if (main.dataset.markdown !== "true" || !window.SlateMarkdownEditor) {
      showSource();
      return;
    }
    try {
      if (!editor) {
        window.SlateMarkdownEditor.ready.catch(() => {});
        editor = window.SlateMarkdownEditor.mount(host, {
          markdown: pre.textContent,
          persist: false,
          persistSettings: false,
          workspaceMode: "preview",
          readOnly: true,
          interactiveTasks: false,
          outlineOpen: false,
          theme: "dark",
          showReadyToast: false,
          resolveUrl: (url, { kind }) => (kind === "image" ? imageUrl(url) : null),
        }).then((api) => {
          const pane = host.querySelector("#previewPane");
          pane.setAttribute("aria-label", "Document contents");
          pane.tabIndex = 0;
          return api;
        });
        await editor;
      } else {
        await (await editor).setMarkdown(pre.textContent);
      }
      await showForm(await editor);
      host.hidden = false;
      pre.hidden = true;
    } catch (error) {
      editor = null;
      showSource();
    }
  }
  // A form template shows its fields as greyed-out boxes: forms are filled in
  // and sent from Slate Viewer, not from this page.
  async function showForm(api) {
    const form = api.getForm();
    if (form && !form.error) {
      await api.startFill({ inPreview: true, readOnly: true, status: "Fill in and send this form from Slate Viewer." });
    } else if (api.isFilling()) {
      api.endFill();
    }
  }
  // The rendered document scrolls inside Slate's preview pane, which stays
  // the same element from file to file, so each file's position is kept
  // here by path: a file opened before comes back where it was left, a new
  // one starts at the top, and a link with #anchor goes to that heading.
  const positions = new Map();
  let shown = pathOf(window.location.href);
  function pathOf(url) {
    const parsed = new URL(url, window.location.href);
    parsed.hash = "";
    return parsed.href;
  }
  function pane() {
    return host.hidden ? null : host.querySelector("#previewPane");
  }
  function saveScroll() {
    const scroller = pane();
    if (scroller) positions.set(shown, scroller.scrollTop);
  }
  // GitHub-style heading anchors; Slate's headings also contain their "##".
  function slug(text) {
    return text.toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu, "").trim().replace(/\s/g, "-");
  }
  function anchorTarget(scroller, hash) {
    let name = hash.slice(1);
    try {
      name = decodeURIComponent(name);
    } catch (error) {
      // keep the raw name
    }
    if (!name) return null;
    for (const element of scroller.querySelectorAll("[id]")) if (element.id === name) return element;
    const wanted = slug(name);
    for (const heading of scroller.querySelectorAll("h1, h2, h3, h4, h5, h6")) {
      if (slug(heading.textContent) === wanted) return heading;
    }
    return null;
  }
  function restoreScroll(url) {
    shown = pathOf(url);
    const scroller = pane();
    if (!scroller) return;
    const target = anchorTarget(scroller, new URL(url, window.location.href).hash);
    if (target) {
      scroller.scrollTop += target.getBoundingClientRect().top - scroller.getBoundingClientRect().top;
    } else {
      scroller.scrollTop = positions.get(shown) || 0;
    }
  }
  render().then(() => restoreScroll(window.location.href));

  // Links inside a document. Slate opens every link in a new tab; pages of
  // this site open in this tab instead (files listed in the tree without a
  // reload), other sites keep the new tab, and mailto: and similar links
  // open normally. Modified clicks are left to the browser.
  let openInPage = null;
  function inTree(url) {
    if (!tree) return false;
    const path = pathOf(url);
    for (const link of tree.querySelectorAll("a[href]")) if (link.href === path) return true;
    return false;
  }
  // [[Wiki links]] open the file of that name in this space, the nearest one
  // to this document when several share it, as in Slate Viewer. Slate itself
  // only stops the click, so this listens before it (capture).
  function spaceSafeName(name) {
    const slug = name
      .normalize("NFKD")
      .replace(/[\u0300-\u036f]/g, "")
      .toLowerCase()
      .replace(/[^a-z0-9._-]+/g, "-")
      .replace(/-{2,}/g, "-")
      .replace(/^[-.]+|-+$/g, "");
    return slug || "note";
  }
  function wikiTarget(name) {
    if (!tree) return null;
    const bare = (path) => path.toLowerCase().replace(/\.(md|markdown|mdown|mkd)$/, "");
    const wanted = bare(name.split("#")[0].trim().replace(/\\/g, "/"));
    if (!wanted) return null;
    const slug = spaceSafeName(wanted.split("/").pop());
    const here = new URL(window.location.href).pathname;
    const hereDir = here.slice(0, here.lastIndexOf("/") + 1);
    let best = null;
    let bestDistance = Infinity;
    for (const link of tree.querySelectorAll("a[href]")) {
      const url = new URL(link.href, window.location.href);
      if (url.origin !== window.location.origin) continue;
      let path;
      try {
        path = decodeURIComponent(url.pathname);
      } catch (error) {
        continue;
      }
      const file = bare(path);
      const base = file.slice(file.lastIndexOf("/") + 1);
      if (!file.endsWith("/" + wanted) && (wanted.includes("/") || base !== slug)) continue;
      const dir = url.pathname.slice(0, url.pathname.lastIndexOf("/") + 1);
      const distance = dir === hereDir ? 0 : dir.startsWith(hereDir) ? dir.length - hereDir.length : 1000 + dir.length;
      if (distance < bestDistance) {
        best = url.href;
        bestDistance = distance;
      }
    }
    return best;
  }
  host.addEventListener(
    "click",
    (event) => {
      const wiki = event.target.closest(".wiki-link");
      if (!wiki || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
      const target = wikiTarget(wiki.dataset.noteName || wiki.textContent || "");
      if (!target) return;
      event.preventDefault();
      event.stopPropagation();
      if (openInPage) openInPage(target, true);
      else window.location.assign(target);
    },
    true,
  );

  host.addEventListener("click", (event) => {
    const link = event.target.closest("a[href]");
    if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const raw = link.getAttribute("href") || "";
    if (raw === "" || raw.startsWith("#")) return; // wiki links and in-page anchors
    let url;
    try {
      url = new URL(link.href, window.location.href);
    } catch (error) {
      return;
    }
    if (url.protocol !== "http:" && url.protocol !== "https:") {
      link.removeAttribute("target");
      return;
    }
    if (url.origin !== window.location.origin) return;
    event.preventDefault();
    if (openInPage && inTree(url.href)) {
      if (url.href !== window.location.href) openInPage(url.href, true);
    } else {
      window.location.assign(url.href);
    }
  });

  // The tree scrolls on its own: show the open file's entry in a long tree.
  const current = tree && tree.querySelector("a[aria-current]");
  if (current) current.scrollIntoView({ block: "nearest" });

  if (!tree || !window.fetch || !window.DOMParser || !window.history.pushState) return;

  function openAncestors(link) {
    for (let node = link.parentElement; node && node !== tree; node = node.parentElement) {
      if (node.tagName === "DETAILS") node.open = true;
    }
  }
  function markCurrent(url) {
    for (const link of tree.querySelectorAll("a[aria-current]")) link.removeAttribute("aria-current");
    for (const link of tree.querySelectorAll("a[href]")) {
      if (link.href === url) {
        link.setAttribute("aria-current", "page");
        openAncestors(link);
      }
    }
  }

  let pending = null;
  async function open(url, push) {
    if (pending) pending.abort();
    const request = new AbortController();
    pending = request;
    main.setAttribute("aria-busy", "true");
    try {
      const response = await fetch(url, { credentials: "same-origin", signal: request.signal, headers: { Accept: "text/html" } });
      if (!response.ok || response.redirected) throw new Error("not a document"); // e.g. signed out
      const page = new DOMParser().parseFromString(await response.text(), "text/html");
      const next = page.getElementById("main");
      if (!next || !next.classList.contains("document")) throw new Error("not a document");
      saveScroll();
      for (const selector of [".tab", ".document-heading", "#document-source"]) {
        const from = next.querySelector(selector);
        const to = main.querySelector(selector);
        if (!from || !to) throw new Error("unexpected page");
        to.replaceChildren(...Array.from(from.childNodes, (child) => document.importNode(child, true)));
      }
      main.dataset.markdown = next.dataset.markdown || "false";
      document.title = page.title;
      if (push) window.history.pushState(null, "", url);
      markCurrent(pathOf(url));
      await render();
      restoreScroll(url);
      main.scrollIntoView({ block: "start" });
    } catch (error) {
      if (error.name === "AbortError") return;
      window.location.assign(url); // fall back to an ordinary page load
    } finally {
      if (pending === request) {
        pending = null;
        main.removeAttribute("aria-busy");
      }
    }
  }

  openInPage = open;

  tree.addEventListener("click", (event) => {
    const link = event.target.closest("a[href]");
    if (!link || event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
    const url = new URL(link.href, window.location.href);
    if (url.origin !== window.location.origin) return;
    event.preventDefault();
    if (url.href !== window.location.href) open(url.href, true);
  });
  window.addEventListener("popstate", () => open(window.location.href, false));
})();
