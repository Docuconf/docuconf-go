package examples

#RateLimitsSchema:

	close({
		perMinute!: int & >=1
		burst?:     int & >=0
	})
