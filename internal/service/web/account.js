"use strict";

// Your account's small panels (details.pop: make readable on the web, remove,
// rename, revoke). Only one is open at a time; a click outside it or Escape
// closes it. Without JavaScript they still open and close with their icon.
(() => {
  const pops = () => document.querySelectorAll("details.pop[open]");
  document.addEventListener("toggle", (event) => {
    const opened = event.target;
    if (!(opened instanceof HTMLDetailsElement) || !opened.matches(".pop") || !opened.open) return;
    for (const other of pops()) if (other !== opened) other.open = false;
  }, true);
  document.addEventListener("click", (event) => {
    for (const pop of pops()) if (!pop.contains(event.target)) pop.open = false;
  });
  document.addEventListener("keydown", (event) => {
    if (event.key !== "Escape") return;
    for (const pop of pops()) {
      pop.open = false;
      pop.querySelector("summary").focus();
    }
  });
})();
