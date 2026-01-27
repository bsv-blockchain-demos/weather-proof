# Validation Plan

This document describes how to verify that Weather Chain meets its design objectives as defined in PLAN.md and SPEC.md.

## Core Objectives

| ID | Objective | Source |
|----|-----------|--------|
| O1 | Source live weather data from Tempest API | SPEC.md |
| O2 | Encode weather data using WeatherDataEncoder | SPEC.md |
| O3 | Store encoded data on BSV blockchain as OP_RETURN outputs | SPEC.md |
| O4 | Use hash puzzle funding basket for transaction fees | SPEC.md |
| O5 | Queue data in MongoDB for async processing | SPEC.md |
| O6 | Monitor funding basket and auto-refill | SPEC.md |
| O7 | Notify operator on funding exhaustion | SPEC.md |

---

## 1. Wallet & Configuration Validation

### 1.1 Environment Configuration

**Verify:** All required environment variables are validated on startup.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Missing `SERVER_PRIVATE_KEY` | Start app without key | App fails with clear error message |
| Missing `TEMPEST_API_KEY` | Start app without key | App fails with clear error message |
| Missing `MONGO_URI` | Start app without URI | App fails with clear error message |
| Invalid `POLL_RATE` | Set non-numeric value | App fails or uses default (300s) |
| Valid configuration | Provide all required vars | App starts successfully |

### 1.2 Wallet Initialization

**Verify:** Wallet connects to storage provider and is ready for operations.

```bash
# Test wallet connection
npm run dev -- --test-wallet
```

| Test | Method | Expected Result |
|------|--------|-----------------|
| Valid private key | Use hex-encoded 32-byte key | Wallet initializes |
| Invalid private key | Use malformed key | Clear error message |
| Wallet storage unreachable | Use invalid `WALLET_STORAGE_URL` | Timeout with error |

---

## 2. Hash Puzzle Funding Basket Validation

### 2.1 Funding Output Creation

**Verify:** Setup creates hash puzzle outputs with stored preimages.

```bash
# Run setup to create funding outputs
npm run setup
```

| Test | Method | Expected Result |
|------|--------|-----------------|
| Create 1000 outputs | Run setup script | 1000 outputs in "funding" basket |
| Hash puzzle structure | Inspect locking script | `OP_SHA256 <hash> OP_EQUAL` format |
| Preimage stored | Check `customInstructions` | 32-byte hex preimage present |
| Preimage valid | SHA256(preimage) == hash | Hash matches locking script |

**Verification Script:**
```typescript
// Verify funding outputs are correctly structured
const { outputs } = await wallet.listOutputs({ basket: 'funding', spendable: true });

for (const output of outputs) {
  const preimage = Buffer.from(output.customInstructions, 'hex');
  const expectedHash = Hash.sha256(preimage);
  
  // Parse locking script and extract hash
  const script = Script.fromHex(output.lockingScript);
  const actualHash = script.chunks[1].data;
  
  assert(Buffer.compare(expectedHash, actualHash) === 0, 'Preimage does not match hash');
}
```

### 2.2 Funding Output Spendability

**Verify:** Funding outputs can be unlocked with stored preimage.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Unlock funding output | Create tx using preimage | Transaction broadcasts successfully |
| Wrong preimage | Use incorrect preimage | Transaction rejected |

---

## 3. Tempest API Integration Validation

### 3.1 Station Discovery

**Verify:** API returns station list and data is parseable.

```bash
curl "https://swd.weatherflow.com/swd/rest/stations?token=${TEMPEST_API_KEY}"
```

| Test | Method | Expected Result |
|------|--------|-----------------|
| Valid API key | Call stations endpoint | Array of station objects |
| Invalid API key | Use wrong key | 401/403 error handled |
| Extract station IDs | Parse `stations[].station_id` | Array of numeric IDs |

### 3.2 Weather Data Retrieval

**Verify:** Forecast endpoint returns data compatible with WeatherDataEncoder.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Fetch forecast | Call better_forecast endpoint | JSON with `current_conditions` |
| Data mapping | Map to WeatherData type | All required fields present |
| Missing fields | Handle partial API response | Graceful fallback/defaults |

### 3.3 Data Mapping Validation

**Verify:** Tempest API response correctly maps to WeatherData schema.

| Tempest Field | WeatherData Field | Validation |
|---------------|-------------------|------------|
| `air_temperature` | `temperature` | Number in valid range |
| `relative_humidity` | `humidity` | 0-100 |
| `station_pressure` | `pressure` | Reasonable atmospheric range |
| `wind_avg` | `windSpeed` | Non-negative number |
| `wind_direction` | `windDirection` | 0-360 |
| `timestamp` | `timestamp` | Valid Unix timestamp |

---

## 4. MongoDB Queue Validation

### 4.1 Record Insertion

**Verify:** Weather data is queued with correct status.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Insert record | Poll Tempest API | Record created with `status: 'pending'` |
| Record structure | Query MongoDB | Contains `stationId`, `timestamp`, `data` |
| Duplicate handling | Same station, same minute | Handled appropriately |

### 4.2 Record Status Transitions

**Verify:** Records follow correct state machine.

```
pending → processing → completed
              ↓
           failed → pending (retry)
```

| Test | Method | Expected Result |
|------|--------|-----------------|
| Processing lock | Start processing | Status changes to `processing` |
| Success | Transaction confirms | Status `completed`, `txid` set |
| Failure | Transaction fails | Status reverts to `pending` |

---

## 5. Transaction Creation Validation

### 5.1 Weather Output Structure

**Verify:** Transactions contain correctly encoded weather data.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Output count | Create tx with 100 records | 100 OP_RETURN outputs |
| Satoshi value | Check output amount | 0 satoshis per output |
| Script format | Parse locking script | Valid OP_RETURN with encoded data |
| Decodable | Use WeatherDataDecoder | Original data recovered |

### 5.2 Funding Input Usage

**Verify:** Transactions correctly spend funding basket outputs.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Input basket | Check input source | From "funding" basket |
| Unlocking script | Verify script structure | Contains valid preimage |
| Single input | Count inputs | One funding input per tx |

### 5.3 Broadcast & Confirmation

**Verify:** Transactions successfully broadcast to network.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Broadcast | Call createAction | Returns `txid` |
| On-chain | Query blockchain | Transaction exists |
| Outputs accessible | Fetch raw tx | Weather outputs present |

---

## 6. Monitoring Service Validation

### 6.1 Basket Level Monitoring

**Verify:** Monitor correctly tracks funding output count.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Above threshold | 500 outputs available | No action taken |
| At threshold | 200 outputs remaining | Refill triggered |
| Below threshold | 50 outputs remaining | Refill triggered |

### 6.2 Auto-Refill

**Verify:** Refill creates new funding outputs when needed.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Refill triggered | Drop below 200 | 1000 new outputs created |
| Refill count | Query basket after refill | ~1200 outputs |
| Refill structure | Check new outputs | Valid hash puzzles with preimages |

### 6.3 Insufficient Funds Handling

**Verify:** Low balance triggers operator notification.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Insufficient funds | Empty wallet, trigger refill | Notification sent |
| Notification content | Check notification | Clear "insufficient funds" message |
| Processing pause | Check processor | Pauses until funding available |

---

## 7. Notification Service Validation

### 7.1 Console Notification (Default)

**Verify:** Console logger outputs messages correctly.

| Test | Method | Expected Result |
|------|--------|-----------------|
| Info message | `sendInfo()` | Logged with INFO level |
| Warning message | `sendWarning()` | Logged with WARN level |
| Error message | `sendError()` | Logged with ERROR level |

### 7.2 Notification Interface

**Verify:** Interface can be implemented by different providers.

```typescript
interface NotificationService {
  sendWarning(message: string): Promise<void>;
  sendError(message: string): Promise<void>;
  sendInfo(message: string): Promise<void>;
}
```

---

## 8. End-to-End Validation

### 8.1 Full Pipeline Test

**Objective:** Verify complete data flow from Tempest API to blockchain.

**Steps:**
1. Start application with valid configuration
2. Wait for first poll cycle (POLL_RATE)
3. Verify records appear in MongoDB with `status: 'pending'`
4. Wait for processor cycle (3 seconds)
5. Verify records updated to `status: 'completed'`
6. Verify `txid` and `outputIndex` populated
7. Fetch transaction from blockchain
8. Decode weather outputs using WeatherDataDecoder
9. Compare decoded data with original MongoDB records

**Success Criteria:**
- [ ] Records created in MongoDB
- [ ] Transactions broadcast successfully
- [ ] Decoded blockchain data matches source data
- [ ] No data loss during processing

### 8.2 Extended Run Test

**Objective:** Verify system stability over time.

**Duration:** 24 hours minimum

**Metrics to Track:**
| Metric | Target |
|--------|--------|
| Records processed | >0 failures per 1000 |
| Transaction rate | ~1 tx per 3 seconds |
| Funding basket | Never reaches 0 |
| Memory usage | Stable (no leaks) |
| API errors | Handled without crash |

### 8.3 Recovery Test

**Objective:** Verify system recovers from failures.

| Scenario | Recovery Expectation |
|----------|---------------------|
| MongoDB restart | Reconnects, resumes processing |
| Tempest API outage | Retries, no data loss |
| Wallet storage outage | Retries, queue builds up |
| Application restart | Resumes from pending records |

---

## 9. Data Integrity Validation

### 9.1 Encoder/Decoder Round-Trip

**Verify:** Encoded data can be fully recovered.

```typescript
const original: WeatherData = { /* test data */ };
const encoded = encoder.encode(original);
const decoded = decoder.decode(encoded);

assert.deepEqual(decoded, original);
```

### 9.2 Blockchain Data Verification

**Verify:** Data on blockchain matches source.

```typescript
// Fetch transaction from blockchain
const tx = await fetchTransaction(txid);
const output = tx.outputs[outputIndex];
const decoded = decoder.decode(Script.fromHex(output.lockingScript));

// Compare with MongoDB record
const record = await WeatherRecord.findOne({ txid, outputIndex });
assert.deepEqual(decoded, record.data);
```

---

## 10. Performance Validation

### 10.1 Throughput

| Metric | Target | Validation Method |
|--------|--------|-------------------|
| Poll rate | 1 per POLL_RATE seconds | Timer accuracy check |
| Processing rate | 100 outputs per tx | Count outputs per tx |
| Transaction rate | ~1 tx per 3 seconds | Measure over 1 hour |

### 10.2 Resource Usage

| Resource | Limit | Validation Method |
|----------|-------|-------------------|
| Memory | <500MB | Monitor process |
| MongoDB connections | Pool size | Connection stats |
| Open file handles | <1000 | `lsof` check |

---

## 11. Security Validation

### 11.1 Key Management

| Test | Method | Expected Result |
|------|--------|-----------------|
| Key not logged | Check all log output | No private key exposure |
| Key not in responses | Check API responses | No key leakage |
| Preimage security | Check storage | Only in wallet storage |

### 11.2 API Key Protection

| Test | Method | Expected Result |
|------|--------|-----------------|
| TEMPEST_API_KEY hidden | Check logs/errors | Key masked in output |
| No hardcoded keys | Code review | All keys from env |

---

## Validation Checklist

### Pre-Deployment

- [ ] All unit tests pass (`npm test`)
- [ ] Environment variables documented
- [ ] Wallet initialized with sufficient funds
- [ ] MongoDB accessible
- [ ] Tempest API credentials valid

### Post-Deployment

- [ ] Funding basket populated (≥1000 outputs)
- [ ] First poll cycle completes
- [ ] First transaction broadcasts
- [ ] Monitoring active
- [ ] Notifications working

### Ongoing

- [ ] Funding basket maintained above threshold
- [ ] No processing backlog
- [ ] Error rate within tolerance
- [ ] Data integrity verified

---

## Test Commands

```bash
# Run all unit tests
npm test

# Verify wallet connection
npm run dev -- --test-wallet

# Create initial funding basket
npm run setup

# Start application
npm start

# Check funding basket status
npm run check-funding

# Verify recent transactions
npm run verify-txs
```

---

## Acceptance Criteria Summary

The system is validated when:

1. **Funding basket** contains hash puzzle outputs with valid preimages
2. **Tempest API** data flows into MongoDB queue
3. **Processor** creates transactions at target rate (~1/3s)
4. **Weather data** on blockchain decodes to original values
5. **Monitor** refills funding basket automatically
6. **Notifications** alert on funding exhaustion
7. **System** runs stable for 24+ hours without intervention
