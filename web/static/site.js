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
    ["[data-side-1-name]", selected.dataset.sideOneName || "Side 1"],
    ["[data-side-1-players]", selected.dataset.sideOnePlayers || "No players assigned"],
    ["[data-side-2-name]", selected.dataset.sideTwoName || "Side 2"],
    ["[data-side-2-players]", selected.dataset.sideTwoPlayers || "No players assigned"],
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

function participantRule(format) {
  if (format === "singles") {
    return { count: 1, exact: true, message: "Singles require exactly one player per side." };
  }
  if (["fourball", "foursomes", "scramble"].includes(format)) {
    return { count: 2, exact: true, message: "This 2v2 format requires exactly two players per side." };
  }
  return { count: 1, exact: false, message: "Assign at least one player per side." };
}

function filterMatchParticipants(form) {
  ["one", "two"].forEach((sideName) => {
    const team = form.querySelector(`[data-match-team="${sideName}"]`);
    const players = form.querySelector(`[data-match-side="${sideName}"]`);
    if (!(team instanceof HTMLSelectElement) || !(players instanceof HTMLSelectElement)) {
      return;
    }
    Array.from(players.options).forEach((option) => {
      const available = option.dataset.teamId === team.value;
      option.hidden = !available;
      option.disabled = !available;
      if (!available) {
        option.selected = false;
      }
    });
  });
}

function assignParticipantDefaults(form) {
  const format = form.querySelector("[data-match-format]");
  const sides = [form.querySelector('[data-match-side="one"]'), form.querySelector('[data-match-side="two"]')];
  if (!(format instanceof HTMLSelectElement) || sides.some((side) => !(side instanceof HTMLSelectElement))) {
    return;
  }
  const rule = participantRule(format.value);
  const used = new Set();
  const original = sides.map((side) => Array.from(side.selectedOptions).map((option) => option.value));
  sides.forEach((side, sideIndex) => {
    let keep = Array.from(side.selectedOptions).map((option) => option.value).filter((value) => !used.has(value));
    const targetCount = rule.exact ? rule.count : Math.max(rule.count, keep.length);
    if (rule.exact && keep.length > targetCount) {
      keep = keep.slice(0, targetCount);
    }
    const reservedForOtherSide = new Set(original.flatMap((values, index) => index === sideIndex ? [] : values));
    Array.from(side.options).forEach((option) => {
      if (option.disabled) {
        return;
      }
      if (keep.length < targetCount && !keep.includes(option.value) && !used.has(option.value) && !reservedForOtherSide.has(option.value)) {
        keep.push(option.value);
      }
    });
    Array.from(side.options).forEach((option) => { option.selected = keep.includes(option.value); });
    keep.forEach((value) => used.add(value));
  });
}

function validateMatchParticipants(form) {
  const format = form.querySelector("[data-match-format]");
  const sides = [form.querySelector('[data-match-side="one"]'), form.querySelector('[data-match-side="two"]')];
  const hint = form.querySelector("[data-match-participant-rule]");
  if (!(format instanceof HTMLSelectElement) || sides.some((side) => !(side instanceof HTMLSelectElement))) {
    return;
  }
  const rule = participantRule(format.value);
  if (hint instanceof HTMLElement) {
    hint.textContent = rule.message + " Add missing players to their team roster first.";
  }
  const selected = sides.map((side) => Array.from(side.selectedOptions).map((option) => option.value));
  const duplicates = selected[0].some((value) => selected[1].includes(value));
  sides.forEach((side, index) => {
    const countInvalid = rule.exact ? selected[index].length !== rule.count : selected[index].length < rule.count;
    side.setCustomValidity(duplicates ? "A player cannot appear on both sides." : countInvalid ? rule.message : "");
  });
  const teams = [form.querySelector('[data-match-team="one"]'), form.querySelector('[data-match-team="two"]')];
  if (teams.every((team) => team instanceof HTMLSelectElement)) {
    const sameTeam = teams[0].value === teams[1].value;
    teams.forEach((team) => team.setCustomValidity(sameTeam ? "Choose two different teams." : ""));
  }
}

document.querySelectorAll("[data-match-create-form]").forEach((form) => {
  if (!(form instanceof HTMLFormElement)) {
    return;
  }
  filterMatchParticipants(form);
  assignParticipantDefaults(form);
  validateMatchParticipants(form);
  form.addEventListener("change", (event) => {
    if (!(event.target instanceof Element)) {
      return;
    }
    if (event.target.matches("[data-match-team]")) {
      filterMatchParticipants(form);
    }
    if (event.target.matches("[data-match-format], [data-match-team]")) {
      assignParticipantDefaults(form);
    }
    if (event.target.matches("[data-match-format], [data-match-team], [data-match-side]")) {
      validateMatchParticipants(form);
    }
  });
  form.addEventListener("submit", () => validateMatchParticipants(form));
});
