// Runs before Slate Viewer's UI starts on the site (/app/).
//
// The saved theme, before anything paints; the Viewer keeps it up to date
// after. The same as the Viewer's index.html.
try {
  const pref = localStorage.getItem('slate-viewer.theme');
  const dark = pref === 'dark' || (pref !== 'light' && pref !== 'paper' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.dataset.theme = pref === 'paper' ? 'paper' : dark ? 'dark' : 'light';
} catch {}

// The editor (CodeMirror, inside Slate) adds its styles in <style> elements,
// which the page's CSP allows only with this page's nonce. Every <style>
// made from here on carries it.
(() => {
  const nonce = document.currentScript && document.currentScript.nonce;
  if (!nonce) return;
  const create = Document.prototype.createElement;
  Document.prototype.createElement = function (name, options) {
    const element = create.call(this, name, options);
    if (String(name).toLowerCase() === 'style') element.nonce = nonce;
    return element;
  };
})();
