import { Transaction, WhatsOnChain } from '@bsv/sdk';

/**
 * Result of verification
 */
export interface VerificationResult {
  verified: boolean;
  txid: string;
  blockHeight?: number;
  error?: string;
}

/**
 * Get the BSV network from environment
 */
export function getNetwork(): 'main' | 'test' {
  const network = import.meta.env.VITE_BSV_NETWORK;
  return network === 'main' ? 'main' : 'test';
}

/**
 * Verify a weather proof using the BEEF format
 * Uses tx.verify() with WhatsOnChain as the chain tracker
 */
export async function verifyWeatherProof(beefHex: string): Promise<VerificationResult> {
  try {
    // Parse BEEF to get Transaction with MerklePath
    const tx = Transaction.fromHexBEEF(beefHex);

    // rely only on merklePath of current tx
    if (!tx.merklePath) {
      throw new Error('Transaction does not have a merkle path');
    }

    // Create WhatsOnChain chain tracker
    const network = getNetwork();
    const chainTracker = new WhatsOnChain(network);

    // Verify the transaction
    // This validates:
    // - Merkle proof is valid (transaction is in a mined block)
    // - Block header hash matches chain tracker's known headers
    const isValid = await tx.verify(chainTracker);

    return {
      verified: isValid,
      txid: tx.id('hex'),
      blockHeight: tx.merklePath?.blockHeight,
    };
  } catch (error) {
    const message = error instanceof Error ? error.message : 'Unknown error';
    return {
      verified: false,
      txid: '',
      error: message,
    };
  }
}
