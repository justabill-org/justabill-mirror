import type { GAOReport } from "@/lib/types";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDate } from "@/lib/utils";

interface GAOReportsListProps {
  reports: GAOReport[];
}

export function GAOReportsList({ reports }: GAOReportsListProps) {
  if (reports.length === 0) {
    return (
      <Card>
        <CardHeader>
          <CardTitle className="text-lg">GAO reports</CardTitle>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            No GAO reports have been linked to this bill.
          </p>
        </CardContent>
      </Card>
    );
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-lg">GAO reports</CardTitle>
      </CardHeader>
      <CardContent>
        <div className="space-y-3">
          {reports.map((report) => (
            <GAOReportCard key={report.report_id} report={report} />
          ))}
        </div>
      </CardContent>
    </Card>
  );
}

function GAOReportCard({ report }: { report: GAOReport }) {
  const publishedDate = report.published_date
    ? formatDate(report.published_date)
    : null;

  return (
    <div className="rounded-lg border border-border p-4">
      <div className="flex flex-wrap items-start justify-between gap-3 mb-2">
        <div className="flex-1 min-w-0">
          <h3 className="text-sm font-medium text-foreground leading-snug">
            {report.title}
          </h3>
        </div>
        {report.report_type && (
          <Badge variant="outline" className="shrink-0">
            {report.report_type}
          </Badge>
        )}
      </div>

      <div className="flex flex-wrap items-center gap-3 text-xs text-muted-foreground mb-2">
        {report.report_number && (
          <span className="font-mono">{report.report_number}</span>
        )}
        {publishedDate && <span>Published {publishedDate}</span>}
      </div>

      {report.summary && (
        <p className="text-sm text-muted-foreground line-clamp-3 mb-3">
          {report.summary}
        </p>
      )}

      <div className="flex gap-3">
        {report.pdf_url && (
          <a
            href={report.pdf_url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs font-medium text-link hover:underline"
          >
            View PDF
          </a>
        )}
        {report.html_url && (
          <a
            href={report.html_url}
            target="_blank"
            rel="noopener noreferrer"
            className="text-xs font-medium text-link hover:underline"
          >
            View HTML
          </a>
        )}
      </div>
    </div>
  );
}
