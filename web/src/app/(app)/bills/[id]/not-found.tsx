import { StatusPage } from "@/components/layout/status-page";

// An unknown or malformed bill ID (#73). Bill IDs are type-congress-number, e.g. hr-119-1.
export default function BillNotFound() {
  return (
    <StatusPage
      code="404"
      title="Bill not found"
      description="We couldn't find that bill. Bill addresses look like /bills/hr-119-1: the bill type, the congress and the number."
      links={[
        { href: "/bills", label: "Browse bills" },
        { href: "/", label: "Go home" },
      ]}
    />
  );
}
