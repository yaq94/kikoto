import { useState } from "react";
import { useTranslation } from "react-i18next";

import {
  creatorWorkSortValues,
  readCreatorWorksBrowse,
  writeCreatorWorksBrowse,
  type CreatorWorkPageSize,
  type CreatorWorksBrowse,
  type CreatorWorksKind,
} from "@/lib/creatorWorksBrowse";
import type { LibrarySortProps } from "@/pages/library/LibrarySortButton";

/**
 * The stored order and page size of a creator's works, with the props the
 * Library sort button takes. `onChange` runs after every change so the caller
 * can return to the first page.
 */
export function useCreatorWorksBrowse(kind: CreatorWorksKind, storageScope: string, onChange: () => void) {
  const { t } = useTranslation();
  const [browse, setBrowse] = useState(() => readCreatorWorksBrowse(kind, storageScope));
  const change = (patch: Partial<CreatorWorksBrowse>) => {
    const next = { ...browse, ...patch };
    setBrowse(next);
    writeCreatorWorksBrowse(kind, storageScope, next);
    onChange();
  };
  const sortProps: LibrarySortProps = {
    options: creatorWorkSortValues.map((value) => ({ value, label: t(`library.sortOptions.${value}`) })),
    value: browse.sort,
    direction: browse.direction,
    onChange: (value) => {
      const sort = creatorWorkSortValues.find((option) => option === value);
      if (sort) change({ sort });
    },
    onDirectionChange: (direction) => change({ direction }),
    // No creator order draws from a seed.
    onReshuffle: () => undefined,
  };
  return {
    ...browse,
    sortProps,
    changePageSize: (value: number) => change({ pageSize: value as CreatorWorkPageSize }),
  };
}
