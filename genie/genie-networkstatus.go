package genie

import (
	"fmt"
	"github.com/Huawei-PaaS/CNI-Genie/utils"
	"github.com/containernetworking/cni/pkg/types/current"
	"os"
	"strconv"
)

func setGenieStatus(result current.Result, ifName string, currStatus interface{}) interface{} {
	var multiIPPreferences utils.MultiIPPreferences
	var ok bool
	if currStatus == nil {
		multiIPPreferences.MultiEntry = 1
		multiIPPreferences.Ips = make(map[string]utils.IPAddressPreferences)
	} else {
		multiIPPreferences, ok = currStatus.(utils.MultiIPPreferences)
		if !ok {
			fmt.Fprintf(os.Stderr, "CNI Genie in setGenieStatus unable to assert multiIPPreferences\n")
			return nil
		}
		multiIPPreferences.MultiEntry = multiIPPreferences.MultiEntry + 1
	}

	if len(result.IPs) == 0 {
		fmt.Fprintf(os.Stderr, "CNI Genie in setGenieStatus no ip in result\n")
		return nil
	}
	multiIPPreferences.Ips["ip"+strconv.Itoa(int(multiIPPreferences.MultiEntry))] = utils.IPAddressPreferences{
		Ip:        result.IPs[0].Address.IP.String(),
		Interface: ifName,
	}
	return interface{}(multiIPPreferences)
}
