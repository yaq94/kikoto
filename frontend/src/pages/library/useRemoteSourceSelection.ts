import { useEffect, useState } from "react";

import { retainVisibleSelection, withSelection } from "@/components/work-collection/workSelectionModel";
import type { RemoteWork } from "@/lib/api";

import { remoteWorkRowKey } from "./remoteSourceBrowseModel";

export function useRemoteSourceSelection({
  selectableWorks,
  visibleWorks,
  selectionMode,
  loading,
  page,
  totalPages,
  onPageChange,
}: {
  selectableWorks: RemoteWork[];
  visibleWorks: RemoteWork[];
  selectionMode: boolean;
  loading: boolean;
  page: number;
  totalPages: number;
  onPageChange: (page: number) => void;
}) {
  const [bulkCodes, setBulkCodes] = useState<Set<string>>(new Set());

  useEffect(() => {
    setBulkCodes((current) => retainVisibleSelection(current, visibleWorks, remoteWorkRowKey));
  }, [visibleWorks]);

  useEffect(() => {
    if (!selectionMode) setBulkCodes(new Set());
  }, [selectionMode]);

  useEffect(() => {
    if (loading || page <= totalPages) return;
    onPageChange(totalPages);
  }, [loading, onPageChange, page, totalPages]);

  const toggleBulkCode = (rowKey: string, checked: boolean) => {
    setBulkCodes((current) => withSelection(current, [rowKey], checked));
  };
  const toggleAllVisible = (checked: boolean) => {
    setBulkCodes(checked ? new Set(selectableWorks.map(remoteWorkRowKey)) : new Set());
  };
  const clearSelection = () => setBulkCodes(new Set());

  return { bulkCodes, toggleBulkCode, toggleAllVisible, clearSelection };
}
