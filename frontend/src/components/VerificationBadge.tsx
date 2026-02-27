import type { VerificationResult } from '../services/verify';
import type { RecordStatus } from '../types/weather';

interface VerificationBadgeProps {
  status: RecordStatus;
  verificationResult?: VerificationResult | null;
  isVerifying?: boolean;
  onVerify?: () => void;
  compact?: boolean;
  isConfirmed?: boolean;
}

export function VerificationBadge({
  status,
  verificationResult,
  isVerifying,
  onVerify,
  compact = false,
  isConfirmed = true,
}: VerificationBadgeProps) {
  // Show status badge for non-completed records
  if (status !== 'completed') {
    const statusStyles: Record<RecordStatus, string> = {
      pending: 'bg-yellow-900/50 text-yellow-400 border border-yellow-700/50',
      processing: 'bg-blue-900/50 text-blue-400 border border-blue-700/50',
      completed: 'bg-emerald-900/50 text-emerald-400 border border-emerald-700/50',
      failed: 'bg-red-900/50 text-red-400 border border-red-700/50',
    };

    return (
      <span className={`inline-flex items-center px-2 py-1 rounded text-xs font-medium ${statusStyles[status]}`}>
        {status}
      </span>
    );
  }

  // Show verification status for completed records
  if (isVerifying) {
    return (
      <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-gray-700 text-gray-300">
        <svg className="animate-spin -ml-0.5 mr-1.5 h-3 w-3" fill="none" viewBox="0 0 24 24">
          <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
          <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z" />
        </svg>
        Verifying...
      </span>
    );
  }

  if (verificationResult) {
    if (verificationResult.verified) {
      return (
        <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-emerald-900/50 text-emerald-400 border border-emerald-700/50">
          <svg className="w-3 h-3 mr-1" fill="currentColor" viewBox="0 0 20 20">
            <path fillRule="evenodd" d="M16.707 5.293a1 1 0 010 1.414l-8 8a1 1 0 01-1.414 0l-4-4a1 1 0 011.414-1.414L8 12.586l7.293-7.293a1 1 0 011.414 0z" clipRule="evenodd" />
          </svg>
          Verified {verificationResult.blockHeight ? `@ Block ${verificationResult.blockHeight.toLocaleString()}` : ''}
        </span>
      );
    } else {
      return (
        <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-red-900/50 text-red-400 border border-red-700/50">
          <svg className="w-3 h-3 mr-1" fill="currentColor" viewBox="0 0 20 20">
            <path fillRule="evenodd" d="M4.293 4.293a1 1 0 011.414 0L10 8.586l4.293-4.293a1 1 0 111.414 1.414L11.414 10l4.293 4.293a1 1 0 01-1.414 1.414L10 11.414l-4.293 4.293a1 1 0 01-1.414-1.414L8.586 10 4.293 5.707a1 1 0 010-1.414z" clipRule="evenodd" />
          </svg>
          Failed
        </span>
      );
    }
  }

  // Show verify button for unverified completed records
  if (onVerify && !compact) {
    return (
      <button
        onClick={onVerify}
        className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-indigo-900/50 text-indigo-400 border border-indigo-700/50 hover:bg-indigo-900/80 transition-colors"
      >
        <svg className="w-3 h-3 mr-1" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />
        </svg>
        Verify
      </button>
    );
  }

  if (!isConfirmed) {
    return (
      <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-yellow-900/50 text-yellow-400 border border-yellow-700/50">
        Pending Confirmation
      </span>
    );
  }

  return (
    <span className="inline-flex items-center px-2 py-1 rounded text-xs font-medium bg-emerald-900/50 text-emerald-400 border border-emerald-700/50">
      On Chain
    </span>
  );
}
