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

document.addEventListener("submit", (event) => {
  if (!(event.target instanceof HTMLFormElement)) {
    return;
  }
  const message = event.target.dataset.confirm;
  if (message && !window.confirm(message)) {
    event.preventDefault();
  }
});

function setFormSectionActive(section, active) {
  if (!(section instanceof HTMLElement)) {
    return;
  }
  section.hidden = !active;
  section.querySelectorAll("input, select, textarea").forEach((control) => {
    if (control instanceof HTMLInputElement || control instanceof HTMLSelectElement || control instanceof HTMLTextAreaElement) {
      control.disabled = !active;
    }
  });
}

function updateMarketCreateForm(form) {
  const type = form.querySelector("[data-market-type]");
  const picker = form.querySelector("[data-match-picker]");
  const matchFields = form.querySelector("[data-market-match-fields]");
  const customFields = form.querySelector("[data-custom-market-fields]");
  const customOutcomes = form.querySelector("[data-custom-outcomes]");
  if (!(type instanceof HTMLSelectElement) || !(picker instanceof HTMLSelectElement)) {
    return;
  }

  const isMatch = type.value === "match";
  setFormSectionActive(matchFields, isMatch);
  setFormSectionActive(customFields, !isMatch);
  setFormSectionActive(customOutcomes, !isMatch);
  picker.required = isMatch;

  if (!isMatch) {
    return;
  }
  const selected = picker.selectedOptions[0];
  const preview = form.querySelector("[data-match-preview]");
  if (!(selected instanceof HTMLOptionElement) || !(preview instanceof HTMLElement) || selected.value === "") {
    if (preview instanceof HTMLElement) {
      preview.hidden = true;
    }
    return;
  }
  preview.hidden = false;
  const values = [
    ["[data-match-title]", selected.dataset.matchTitle || "Match winner"],
    ["[data-side-1-name]", selected.dataset.side1 || "Side 1"],
    ["[data-side-1-players]", selected.dataset.side1Players || "No players assigned"],
    ["[data-side-2-name]", selected.dataset.side2 || "Side 2"],
    ["[data-side-2-players]", selected.dataset.side2Players || "No players assigned"],
  ];
  values.forEach(([selector, value]) => {
    const target = preview.querySelector(selector);
    if (target instanceof HTMLElement) {
      target.textContent = value;
    }
  });
}

document.querySelectorAll("[data-market-create-form]").forEach((form) => {
  if (!(form instanceof HTMLFormElement)) {
    return;
  }
  updateMarketCreateForm(form);
  form.addEventListener("change", (event) => {
    if (event.target instanceof Element && event.target.matches("[data-market-type], [data-match-picker]")) {
      updateMarketCreateForm(form);
    }
  });
});
