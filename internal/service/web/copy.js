"use strict";

// Click (or Enter/Space) on a [data-copy] element copies its value and says so.
// Without JavaScript the addresses stay selectable text.
(() => {
  for (const el of document.querySelectorAll("[data-copy]")) {
    const text = el.dataset.copy;
    const status = document.createElement("span");
    status.className = "copy-status";
    status.setAttribute("role", "status");
    status.setAttribute("aria-live", "polite");
    el.after(status);
    el.tabIndex = 0;
    el.setAttribute("role", "button");
    el.setAttribute("aria-label", "Copy " + text);
    el.title = "Click to copy";
    el.classList.add("copyable");

    let timer;
    const show = (message, ok) => {
      status.textContent = message;
      status.classList.toggle("copy-failed", !ok);
      el.classList.toggle("copied", ok);
      clearTimeout(timer);
      timer = setTimeout(() => {
        status.textContent = "";
        el.classList.remove("copied");
      }, 4000);
    };
    const select = () => {
      const range = document.createRange();
      range.selectNodeContents(el);
      const selection = window.getSelection();
      selection.removeAllRanges();
      selection.addRange(range);
    };
    const copy = async () => {
      try {
        await navigator.clipboard.writeText(text);
        show("Copied to clipboard", true);
        return;
      } catch (_) {
        // Fall back for browsers without the async clipboard API.
      }
      select();
      try {
        if (document.execCommand("copy")) {
          show("Copied to clipboard", true);
          return;
        }
      } catch (_) {
        // Selection stays so the user can copy by hand.
      }
      show("Press Ctrl+C (⌘C on Mac) to copy", false);
    };
    el.addEventListener("click", copy);
    el.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") {
        event.preventDefault();
        copy();
      }
    });
  }
})();
