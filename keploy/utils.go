package keploy

import (
	"fmt"
	"net/http"
)

func compareHeaders(h1 http.Header, h2 http.Header) bool {
	return cmpHeader(h1, h2) && cmpHeader(h2, h1)

}

func cmpHeader(h1 http.Header, h2 http.Header) bool {
	for k, v := range h1 {
		val, ok:= h2[k]
		if !ok {
			fmt.Println("header not present", k)
			return false
		}
		for i, e := range v {
			if val[i] != e {
				fmt.Println("value not same", k, v, val)
				return false
			}
		}
	}
	return true
}
