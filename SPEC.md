# Stack
Use @bsv/sdk and @bsv/wallet-toolbox npm packages to get the BSV stuff done. https://fast.brc.dev/llm-training-guide.txt is a good reference for patterns to use. The service should have a SERVER_PRIVATE_KEY defined as an env variable. This is the basis of the Server Wallet which will connect to a wallet storage provider at WALLET_STORAGE_URL from env.

Use ./src/service/wallet.ts as the correct pattern for wallet initialization.

# Setup Phase
A setup phase will be required to create a funding basket.
The setup will entail createAction method again, with output basket "funding" being used for each new output, the locking script ought to be a hash puzzle.
The hash puzzle will be a SHA256 hash of Random(32) (Random is a class from @bsv/sdk), and the unlocking script will be the random string.
The random string ought to be kept in wallet storage by setting it as the customInstructions property for the output when it's defined within createAction.
These outputs can be created 1000 at a time within the same tx.

# Monitoring and Notifications
A monitor service should periodically check the "funding" basket by using the wallet method listOutputs() - when it drops below 200, another 1000 ought to be created. If there are insufficient funds to create more funding outputs the createAction will fail. Use that as a trigger for a warning to be sent to the service operator. Define this as an interface which could be hooked up to Twilio SMS notifications, or a simple log to console as default.

# Main Service
The main service will source live data from the Tempest API and encode it into a Script output using the WeatherDataEncoder defined in this repository. TEMPEST_API_KEY env variable will grant access to the subset of stations we will get data from.
First make a call to `https://swd.weatherflow.com/swd/rest/stations?token=${TEMPEST_API_KEY}` to get a list of stations to source data from. response_data.stations.map(s => s.station_id) should be used to get the station ids. Then you can call each one using `https://swd.weatherflow.com/swd/rest/better_forecast?station_id=${s.station_id}&token=${TEMPEST_API_KEY}` which responds with data. Then use data.current_conditions as the basis for the WeatherDataEncoder. Documentation for the tempest api can be found here for research purposes: https://apidocs.tempestwx.com/reference/get_better-forecast-1 start with the above guide, but if it doesn't work then use the docs to investigate fixes.

Source data should be added to a queue for async processing. The rate at which we check these endpoints should be once every 300 seconds (configurable via POLL_RATE env variable which is a number in seconds). A separate service takes that queued data and adds it to transactions on the blockchain. If anything fails, the data is still collected as usual for later processing. A MONGO_URI env variable points to a mongo db instance for collecting the source data records prior to processing.

The wallet method createAction() will be used to create the transaction which contains the output with lockingScript from the encoder and satoshis: 0, outputDescription: 'weather'. The inputs used should be one of the outputs returned from listOutputs() method using basket: 'funding'. Ideally one input should be used for each transaction, which means each of those funding outputs ought to be about 1000 satoshis, configurable via an env variable FUNDING_OUTPUT_AMOUNT. That one input could be used to fund 100 outputs within one transaction.
This should land us at one transaction every 3 seconds on average.

Once the transaction is created, the returned txid and corresponding output index is stored in each record of the mongodb collection associated with the source data.