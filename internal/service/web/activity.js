"use strict";

(() => {
  const list = document.getElementById("recent-files");
  const button = document.getElementById("refresh-activity");
  const status = document.getElementById("activity-status");
  if (!list || !button || !status) return;

  const basePath = document.body.dataset.basePath || "";
  let busy = false;
  let timer;
  let delay = 60000;
  const schedule = () => {
    clearTimeout(timer);
    if (!document.hidden) timer = setTimeout(refresh, delay);
  };

  async function refresh() {
    if (busy || document.hidden) return;
    clearTimeout(timer);
    busy = true;
    button.disabled = true;
    list.setAttribute("aria-busy", "true");
    status.textContent = "Refreshing…";
    const controller = new AbortController();
    const timeout = setTimeout(() => controller.abort(), 20000);
    try {
      const response = await fetch(basePath + "/api/v1/spaces/public/recent", {
        signal: controller.signal,
        cache: "no-store",
        headers: { Accept: "application/json" }
      });
      if (!response.ok) {
        const retry = Number(response.headers.get("Retry-After"));
        if (Number.isFinite(retry) && retry > 0) delay = Math.max(delay, retry * 1000);
        throw new Error("Activity request failed");
      }
      const data = await response.json();
      if (typeof data.state !== "string" || !Array.isArray(data.files) || data.files.length > 10) {
        throw new Error("Invalid activity response");
      }
      const fragment = document.createDocumentFragment();
      for (const file of data.files) {
        if (![file.path, file.timestamp, file.date].every(value => typeof value === "string")) {
          throw new Error("Invalid activity entry");
        }
        const item = document.createElement("li");
        const link = document.createElement("a");
        // Construct a fixed local route; never interpret space content as markup or a URL.
        link.href = basePath + "/spaces/public/" + file.path.split("/").map(encodeURIComponent).join("/");
        const path = document.createElement("span");
        path.className = "file-path";
        path.textContent = file.path;
        const time = document.createElement("time");
        time.dateTime = file.timestamp;
        time.textContent = file.date;
        const arrow = document.createElement("span");
        arrow.setAttribute("aria-hidden", "true");
        arrow.textContent = "↗";
        link.append(path, time, arrow);
        item.append(link);
        fragment.append(item);
      }
      if (!data.files.length) {
        const empty = document.createElement("li");
        empty.className = "empty";
        empty.textContent = "No files yet.";
        fragment.append(empty);
      }
      list.replaceChildren(fragment);
      status.textContent = "Updated " + new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
      delay = 60000;
    } catch {
      status.textContent = "Could not refresh. Previous list retained; retrying automatically.";
      delay = Math.min(Math.max(delay * 2, 60000), 900000);
    } finally {
      clearTimeout(timeout);
      busy = false;
      button.disabled = false;
      list.removeAttribute("aria-busy");
      schedule();
    }
  }

  button.addEventListener("click", refresh);
  document.addEventListener("visibilitychange", schedule);
  document.querySelector(".activity-controls").hidden = false;
  // The server supplies the first list; polling is needed only while visible.
  schedule();
})();
