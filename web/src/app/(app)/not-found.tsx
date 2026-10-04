import { StatusPage } from "@/components/layout/status-page";

// notFound() in the (app) pages, e.g. an unknown member (#73).
export default function NotFound() {
  return (
    <StatusPage
      code="404"
      title="Not found"
      description="We couldn't find what you were looking for. It may not exist, or the link may be mistyped."
    />
  );
}
