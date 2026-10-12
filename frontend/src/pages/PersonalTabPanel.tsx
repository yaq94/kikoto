import { lazy, Suspense, type ComponentProps } from "react";

import type { PersonalTab } from "@/pages/personalTabs";

// Each personal tab loads its own chunk so Settings does not download them up front.
const UserTagManagementPage = lazy(() =>
  import("@/features/user-tags/UserTagManagementPage").then((module) => ({ default: module.UserTagManagementPage })),
);
const UserDataPage = lazy(() =>
  import("@/features/user-data/UserDataPage").then((module) => ({ default: module.UserDataPage })),
);

export type PersonalTabProps = {
  tags: ComponentProps<typeof UserTagManagementPage>;
  data: ComponentProps<typeof UserDataPage>;
};

export function PersonalTabPanel({ tab, tags, data }: PersonalTabProps & { tab: PersonalTab }) {
  return (
    <Suspense fallback={null}>
      {tab === "tags" && <UserTagManagementPage {...tags} />}
      {tab === "data" && <UserDataPage {...data} />}
    </Suspense>
  );
}
