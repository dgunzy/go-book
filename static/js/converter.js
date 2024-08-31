// Convert decimal odds to American odds
// function decimalToAmerican(decimal) {
//   if (decimal >= 2) {
//     return "+" + Math.round((decimal - 1) * 100);
//   } else {
//     return Math.round(-100 / (decimal - 1));
//   }
// }

// // Convert American odds to decimal odds
// function americanToDecimal(american) {
//   if (american > 0) {
//     return american / 100 + 1;
//   } else {
//     return 100 / -american + 1;
//   }
// }

// Function to add a new outcome
// function addOutcome() {
//     const container = document.getElementById("betOutcomesContainer");
//     const outcomeDiv = document.createElement("div");
//     outcomeDiv.classList.add("flex", "items-center", "gap-4", "mb-4");
//     outcomeDiv.innerHTML = `
//         <input type="text" name="OutcomeDescription[]" placeholder="Outcome Description" class="p-2 bg-gray-100 rounded" required>
//         <input type="number" step="0.01" name="Odds[]" placeholder="Odds" class="p-2 bg-gray-100 rounded" required>
//         <button type="button" onclick="this.parentElement.remove()" class="bg-red-500 hover:bg-red-700 text-white font-bold py-2 px-4 rounded">Remove</button>
//     `;
//     container.appendChild(outcomeDiv);
// }

// Function to update decimal odds when American odds change
// function updateDecimalOdds(input) {
//   const americanOdds = input.value;
//   const decimalOdds = americanToDecimal(parseFloat(americanOdds));
//   input.nextElementSibling.value = decimalOdds.toFixed(2);
// }

// Function to prepare form data before submission
// function prepareFormData(event) {
//   event.preventDefault();
//   const form = event.target;
//   const americanOddsInputs = form.querySelectorAll(
//     'input[name="AmericanOdds[]"]'
//   );
//   americanOddsInputs.forEach((input) => {
//     updateDecimalOdds(input);
//   });
//   form.submit();
// }
