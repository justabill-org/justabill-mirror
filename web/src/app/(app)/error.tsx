"use client";

import { ErrorPage, type ErrorPageProps } from "@/components/layout/error-page";

// Errors in the (app) pages, rendered inside the group's layout so the navbar stays (#73).
export default function Error(props: ErrorPageProps) {
  return <ErrorPage {...props} />;
}
