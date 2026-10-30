"use strict";
const element = (id) => document.getElementById(id);
async function load(path) {
  const response = await fetch(path, { credentials: "same-origin", cache: "no-store" });
  if (!response.ok) throw new Error(`Request failed (${response.status})`);
  return response.json();
}
async function refresh() {
  element("status").textContent = "Loading…";
  try {
    const [traces, reports] = await Promise.all([load("./traces"), load("./reports")]);
    element("traces").replaceChildren();
    for (const span of traces) {
      const row = document.createElement("tr");
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = span.id;
      button.onclick = () => { element("detail").textContent = JSON.stringify(span, null, 2); };
      const cell = document.createElement("td");
      cell.append(button);
      row.append(cell);
      for (const value of [span.parent_id || "—", span.name, `${new Date(span.ended) - new Date(span.started)} ms`, span.error_type || "—"]) {
        const td = document.createElement("td");
        td.textContent = value;
        row.append(td);
      }
      element("traces").append(row);
    }
    element("reports").replaceChildren();
    for (const name of ["baseline", "candidate"]) element(name).replaceChildren();
    for (const report of reports) {
      const details = document.createElement("details");
      const summary = document.createElement("summary");
      summary.textContent = `${report.id}: ${report.summary.passed}/${report.summary.runs} passed`;
      const text = document.createElement("pre");
      text.textContent = JSON.stringify(report, null, 2);
      details.append(summary, text);
      element("reports").append(details);
      for (const name of ["baseline", "candidate"]) {
        const option = document.createElement("option");
        option.value = report.id;
        option.textContent = report.id;
        element(name).append(option);
      }
    }
    element("compare").disabled = reports.length === 0;
    element("status").textContent = `${traces.length} completed spans; ${reports.length} evaluations`;
  } catch (error) { element("status").textContent = error.message; }
}
element("refresh").onclick = refresh;
element("compare").onclick = async () => {
  try {
    const query = new URLSearchParams({ baseline: element("baseline").value, candidate: element("candidate").value });
    element("comparison").textContent = JSON.stringify(await load(`./compare?${query}`), null, 2);
  } catch (error) { element("comparison").textContent = error.message; }
};
refresh();
