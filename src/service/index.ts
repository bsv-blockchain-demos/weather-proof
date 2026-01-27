// The service will connect to a wallet storage provider using the @bsv/wallet-toolbox library.
// It will source data from the Tempest API and encode it into a Script output using the local WeatherDataEncoder.
// The wallet method createAction() will be used to create the transaction which contains the output of value 0 satoshis.

// A setup phase will be required to create a funding basket.
// The setup will entail createAction method again, with output basket "funding" being used for each new output, the locking script ought to be a hash puzzle.
// The hash puzzle will be a SHA256 hash of a random string, and the unlock script will be the random string.
// The random string ought to be kept in the wallet storage by setting it as the customInstructions property for the output.