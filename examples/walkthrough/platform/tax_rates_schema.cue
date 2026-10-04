package platform

#TaxRates:

	close({
		// Rate used when a country is not listed
		default!: >=0 & <=1

		// Rates by ISO 3166-1 alpha-2 country code
		countries!: [string]: number
	})
