"use client";

import { Analytics } from "@vercel/analytics/next";
import { redactAnalyticsEvent } from "@/lib/analytics";

// A client component because beforeSend is a function, which a server
// component can't pass down. The root layout renders it only in production.
export function WebAnalytics() {
  return <Analytics beforeSend={redactAnalyticsEvent} />;
}
