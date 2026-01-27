import { KeyDeriver, PrivateKey, WalletInterface } from '@bsv/sdk'
import { Services, StorageClient, Wallet, WalletSigner, WalletStorageManager } from '@bsv/wallet-toolbox-client'

const SERVER_PRIVATE_KEY = process.env.SERVER_PRIVATE_KEY ?? 'bcc56b658e5b8660ceba47f323e8a77c4794ab9c76f2bd0082a056c723980049'
const WALLET_STORAGE_URL = process.env.WALLET_STORAGE_URL ?? 'https://store-us-1.bsvb.tech'
const BSV_NETWORK = (process.env.BSV_NETWORK?.length ?? 0) > 0 ? (process.env.BSV_NETWORK as 'main' | 'test') : 'test'

let walletInstance: WalletInterface | null = null

export async function getWallet (): Promise<WalletInterface> {
  if (walletInstance == null) {
    const chain = BSV_NETWORK !== 'test' ? 'main' : 'test'
    const keyDeriver = new KeyDeriver(new PrivateKey(SERVER_PRIVATE_KEY, 'hex'))
    const storageManager = new WalletStorageManager(keyDeriver.identityKey)
    const signer = new WalletSigner(chain, keyDeriver, storageManager)
    const services = new Services(chain)
    const wallet = new Wallet(signer, services)
    const client = new StorageClient(wallet, WALLET_STORAGE_URL)
    await client.makeAvailable()
    await storageManager.addWalletStorageProvider(client)
    walletInstance = wallet
    return wallet
  }
  return walletInstance
}