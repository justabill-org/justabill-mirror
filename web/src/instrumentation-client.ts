// Next.js runs this in the browser before hydration (docs/design/53-observability.md, Decision 5).
// It only adds error listeners; the OpenTelemetry Web SDK loads later, when the browser is idle
// after the page's load event, and not at all with NEXT_PUBLIC_OTEL_DISABLED=true.

import { startBrowserObs } from "@/lib/obs/browser";

try {
  startBrowserObs();
} catch {
  // Telemetry must never break the page.
}
