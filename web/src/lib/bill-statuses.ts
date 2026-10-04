// Where the bills stand, for My votes (#843): every congress's list of bills past a chamber
// (`GET /bill-statuses`, #853), read in the browser.

import { getBillStatuses, listCongresses } from "@/lib/api";
import { NO_BILL_STATUSES, type BillStatuses } from "@/lib/my-votes";
import { reportError } from "@/lib/obs/browser";
import type { BillStatus } from "@/lib/types";

export interface BillStatusesApi {
  listCongresses: typeof listCongresses;
  getBillStatuses: typeof getBillStatuses;
}

/**
 * Reads the list of every congress the site has, not only the ones the visitor voted in, so the
 * requests are the same for everyone and say nothing about their votes. It never rejects: a
 * congress whose list fails is left out, and its bills' statuses stay unknown.
 */
export async function loadBillStatuses(
  api: BillStatusesApi = { listCongresses, getBillStatuses },
): Promise<BillStatuses> {
  let numbers: number[];
  try {
    numbers = (await api.listCongresses()).map((c) => c.number);
  } catch (err) {
    reportError(err);
    return NO_BILL_STATUSES;
  }
  const lists = await Promise.allSettled(numbers.map((n) => api.getBillStatuses(n)));
  const byId: Record<string, BillStatus> = {};
  const congresses = new Set<number>();
  for (const list of lists) {
    if (list.status === "rejected") {
      reportError(list.reason);
      continue;
    }
    congresses.add(list.value.congress);
    for (const bill of list.value.bills) byId[bill.id] = bill.status;
  }
  return { byId, congresses };
}
