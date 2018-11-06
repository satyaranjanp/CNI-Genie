package utils

import (
	"errors"
)

type AggError []error

func (agg AggError) AggregateErrors() error {
	if len(agg) == 0 {
		return nil
	}

	var errmsg string
	for i := 0; i < len(agg); i++ {
		if agg[i] != nil {
			errmsg += "[" + agg[i].Error() + "] "
		}
	}

	if errmsg != "" {
		return errors.New(errmsg)
	}

	return nil
}
