(() => {
  "use strict";

  const host = document.getElementById("document-viewer");
  const source = document.getElementById("document-source");
  if (!host || !source || !window.SlateMarkdownEditor) return;

  // Keep the escaped server-rendered text available if preview cannot start.
  window.SlateMarkdownEditor.ready.catch(() => {});
  window.SlateMarkdownEditor.mount(host, {
    markdown: source.textContent,
    persist: false,
    persistSettings: false,
    workspaceMode: "preview",
    readOnly: true,
    interactiveTasks: false,
    outlineOpen: false,
    theme: "dark",
    showReadyToast: false,
  }).then(() => {
    host.querySelector("#previewPane").setAttribute("aria-label", "Document contents");
    host.querySelector("#previewPane").tabIndex = 0;
    host.hidden = false;
    source.hidden = true;
  }).catch(() => {
    host.hidden = true;
    source.hidden = false;
  });
})();
