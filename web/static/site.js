"use strict";

document.addEventListener("click", (event) => {
  if (!(event.target instanceof Element)) {
    return;
  }
  const target = event.target.closest("[data-select-on-click]");
  if (target instanceof HTMLInputElement) {
    target.select();
  }
});
