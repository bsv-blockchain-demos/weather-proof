import { connectMongo, disconnectMongo } from '../db/connection';
import { getWallet } from '../service/wallet';
import { createFundingOutputs, getFundingOutputCount } from '../service/setup';
import { validateConfig } from '../config/env';

/**
 * Setup script to create initial funding basket
 * Run this before starting the main application
 */
async function setup(): Promise<void> {
  console.log('='.repeat(60));
  console.log('Weather Chain - Funding Basket Setup');
  console.log('='.repeat(60));

  try {
    // Validate configuration
    console.log('Validating configuration...');
    validateConfig();
    console.log('✓ Configuration valid');

    // Connect to MongoDB
    console.log('Connecting to MongoDB...');
    await connectMongo();
    console.log('✓ Connected to MongoDB');

    // Initialize wallet
    console.log('Initializing wallet...');
    await getWallet();
    console.log('✓ Wallet initialized');

    // Check current funding outputs
    console.log('Checking current funding basket...');
    const currentCount = await getFundingOutputCount();
    console.log(`Current funding outputs: ${currentCount}`);

    // Ask for confirmation
    const readline = require('readline').createInterface({
      input: process.stdin,
      output: process.stdout,
    });

    const count = await new Promise<number>((resolve) => {
      readline.question('How many funding outputs to create? (default: 1000): ', (answer: string) => {
        readline.close();
        const num = parseInt(answer, 10);
        resolve(isNaN(num) ? 1000 : num);
      });
    });

    console.log(`Creating ${count} funding outputs...`);
    const txid = await createFundingOutputs(count);

    console.log('='.repeat(60));
    console.log('✓ Setup complete!');
    console.log(`Transaction ID: ${txid}`);
    console.log(`Total funding outputs: ${currentCount + count}`);
    console.log('='.repeat(60));
  } catch (error) {
    console.error('Setup failed:', error);
    process.exit(1);
  } finally {
    await disconnectMongo();
  }
}

// Run setup
setup().catch((error) => {
  console.error('Fatal error:', error);
  process.exit(1);
});
