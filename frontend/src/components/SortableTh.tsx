export type SortDir = 'asc' | 'desc';

interface SortableThProps<K extends string> {
  label: string;
  sortKey: K;
  currentSort: K | null;
  currentDir: SortDir;
  onSort: (key: K) => void;
  right?: boolean;
}

export function SortableTh<K extends string>({
  label,
  sortKey: key,
  currentSort,
  currentDir,
  onSort,
  right,
}: SortableThProps<K>) {
  const isActive = currentSort === key;
  return (
    <th className="px-4 py-3 text-xs font-medium text-gray-400 uppercase tracking-wide">
      <button
        onClick={() => onSort(key)}
        className={`inline-flex items-center gap-1.5 group${right ? ' justify-end w-full' : ''}`}
      >
        <span>{label}</span>
        <span className="inline-flex flex-col gap-[1px]">
          <svg width="8" height="5" viewBox="0 0 8 5" fill="none" className={`transition-colors ${isActive && currentDir === 'asc' ? 'text-indigo-400' : 'text-gray-600 group-hover:text-gray-400'}`}>
            <path d="M1 4L4 1L7 4" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/>
          </svg>
          <svg width="8" height="5" viewBox="0 0 8 5" fill="none" className={`transition-colors ${isActive && currentDir === 'desc' ? 'text-indigo-400' : 'text-gray-600 group-hover:text-gray-400'}`}>
            <path d="M1 1L4 4L7 1" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"/>
          </svg>
        </span>
      </button>
    </th>
  );
}
