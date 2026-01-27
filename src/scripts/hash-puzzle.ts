import { Random, Hash, Script, OP } from '@bsv/sdk';

/**
 * Hash puzzle result containing locking script and preimage
 */
export interface HashPuzzle {
  lockingScript: string;
  preimage: string;
}

/**
 * Create a hash puzzle locking script with SHA256
 * Returns the locking script in hex format and the preimage to unlock it
 *
 * @returns {HashPuzzle} Object containing lockingScript (hex) and preimage (hex)
 *
 * @example
 * const puzzle = createHashPuzzle();
 * // puzzle.lockingScript can be used in transaction outputs
 * // puzzle.preimage should be stored securely for later unlocking
 */
export function createHashPuzzle(): HashPuzzle {
  // Generate 32 random bytes as preimage
  const preimage = Random(32);

  // Hash the preimage with SHA256
  const hash = Hash.sha256(preimage);

  // Create locking script: OP_SHA256 <hash> OP_EQUAL
  const lockingScript = new Script();
  lockingScript.writeOpCode(OP.OP_SHA256);
  lockingScript.writeBin(Array.from(hash));
  lockingScript.writeOpCode(OP.OP_EQUAL);

  return {
    lockingScript: lockingScript.toHex(),
    preimage: Buffer.from(preimage).toString('hex'),
  };
}

/**
 * Create an unlocking script for a hash puzzle
 * The unlocking script simply pushes the preimage onto the stack
 *
 * @param {string} preimage - The preimage in hex format
 * @returns {string} The unlocking script in hex format
 *
 * @example
 * const unlockingScript = createUnlockingScript(puzzle.preimage);
 * // Use this unlocking script when spending the hash puzzle output
 */
export function createUnlockingScript(preimage: string): string {
  const script = new Script();
  script.writeBin(Array.from(Buffer.from(preimage, 'hex')));
  return script.toHex();
}

/**
 * Verify that a preimage correctly unlocks a hash puzzle
 *
 * @param {string} lockingScriptHex - The locking script in hex format
 * @param {string} preimageHex - The preimage in hex format
 * @returns {boolean} True if the preimage is correct
 */
export function verifyHashPuzzle(lockingScriptHex: string, preimageHex: string): boolean {
  try {
    const preimage = Buffer.from(preimageHex, 'hex');
    const hash = Hash.sha256(preimage);

    // Parse the locking script
    const lockingScript = Script.fromHex(lockingScriptHex);

    // The locking script should be: OP_SHA256 <hash> OP_EQUAL
    // Chunk 0: OP_SHA256
    // Chunk 1: hash data
    // Chunk 2: OP_EQUAL

    if (lockingScript.chunks.length < 3) {
      return false;
    }

    const hashFromScript = lockingScript.chunks[1].data;
    if (!hashFromScript) {
      return false;
    }

    return Buffer.from(hashFromScript).equals(Buffer.from(hash));
  } catch (error) {
    return false;
  }
}
