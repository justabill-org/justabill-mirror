// A module that imports React, which a shared module mustn't (src/__tests__/shared-imports.test.ts).
import { useState } from "react";
import { billListParams } from "@/lib/bill-filters";

export const fixture = { useState, billListParams };
