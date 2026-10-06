package examples

#RoutesSchema:

	close({
		routes!: [_, ...] & [...close({
			match!:    =~"^/"
			upstream!: =~"^https?://"
			timeout?:  =~"^([0-9]+(ms|s|m))+$"
		})]
	})
