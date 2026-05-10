// Placeholder per phasing.md — M5 wires real cost data from the usage table
// and `runtime.event{kind=usage}` rows. For M3 we render a single em-dash.

export function CostPanel() {
  return (
    <div className="panel">
      <h3>Cost</h3>
      <div style={{ fontSize: 24, fontFamily: "ui-monospace, monospace" }}>
        —
      </div>
      <div className="empty" style={{ marginTop: 4 }}>
        Cost reporting lands in M5.
      </div>
    </div>
  );
}
