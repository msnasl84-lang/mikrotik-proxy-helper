# VLESS Mode Manager v0.2 for RouterOS 7.24.x
# Requires the existing VLESS-Apply-Coordinator script.
# Change ovpnName below if NetXN is not the preferred OpenVPN connection.

/system/script/remove [find where name="VLESS-Mode-Manager"]
/system/script/add name="VLESS-Mode-Manager" comment="VLESS/OVPN/BLOCKED/DIRECT controller and boot recovery" policy=read,write,test,password,sensitive source={
    :local pfx "VPN-MODE"
    :if ([/system/script/job/print count-only as-value where script=[:jobname]] > 1) do={ :error "mode manager already running" }

    :local desiredPath "helper-data/desired-mode.json"
    :local requestPath "helper-data/mode-request.json"
    :local statusPath "helper-data/router-status.json"
    :local applyPath "helper-data/apply-request.json"
    :local ovpnName "NetXN"
    :local mode "vless"
    :local requested false

    :global VLESS_ACTIVE_PROFILE
    :local oldStatusFile [/file/find where name=$statusPath]
    :if (([:len $VLESS_ACTIVE_PROFILE] = 0) and ([:len $oldStatusFile] = 1)) do={
        :onerror oldStatusError in={
            :local oldStatus [:deserialize from=json value=[/file/get $oldStatusFile contents] options=json.no-string-conversion]
            :set VLESS_ACTIVE_PROFILE ($oldStatus->"active_profile")
        } do={ :log warning ($pfx.": could not restore active profile marker") }
    }

    :local desiredFile [/file/find where name=$desiredPath]
    :if ([:len $desiredFile] = 1) do={
        :onerror e in={
            :local d [:deserialize from=json value=[/file/get $desiredFile contents] options=json.no-string-conversion]
            :if ([:len ($d->"mode")] > 0) do={ :set mode ($d->"mode") }
        } do={ :log warning ($pfx.": invalid desired-mode file; using vless") }
    }

    :local requestFile [/file/find where name=$requestPath]
    :if ([:len $requestFile] = 1) do={
        :local q [:deserialize from=json value=[/file/get $requestFile contents] options=json.no-string-conversion]
        :if (($q->"action") != "set-mode") do={ :error "unsupported mode request" }
        :set mode ($q->"mode")
        :if (($mode != "vless") and ($mode != "ovpn") and ($mode != "blocked") and ($mode != "direct")) do={ :error "invalid requested mode" }
        :set requested true
    }

    :local xray [/container/find where name="xray"]
    :local tun [/container/find where name="tun2socks"]
    :local vlessRoute [/ip/route/find where comment="VLESS via tun2socks"]
    :local ovpn [/interface/ovpn-client/find where name=$ovpnName]
    :local ovpnRoute [/ip/route/find where routing-table="VPN-OUT" and gateway=$ovpnName]
    :if ([:len $xray] != 1) do={ :error "xray container missing or duplicated" }
    :if ([:len $tun] != 1) do={ :error "tun2socks container missing or duplicated" }
    :if ([:len $vlessRoute] != 1) do={ :error "VLESS route missing or duplicated" }

    :if ($mode = "vless") do={
        /interface/ovpn-client/disable [find]
        /ip/route/disable [find where routing-table="VPN-OUT" and (comment="NetX-ovpn" or comment="ZT-Ninja-ovpn")]
        /ip/route/enable $vlessRoute
        :if ([/container/get $xray status] != "running") do={ /container/start $xray; :delay 3s }
        :if ([/container/get $tun status] != "running") do={ /container/start $tun; :delay 3s }
        /system/script/run [find where name="LAN1-KILLSWITCH-ENABLE"]
    }
    :if ($mode = "ovpn") do={
        :if ([:len $ovpn] != 1) do={ :error "selected OVPN client missing or duplicated" }
        :if ([:len $ovpnRoute] != 1) do={ :error "selected OVPN route missing or duplicated" }
        /ip/route/disable $vlessRoute
        :if ([/container/get $tun status] = "running") do={ /container/stop $tun }
        :if ([/container/get $xray status] = "running") do={ /container/stop $xray }
        /interface/ovpn-client/disable [find]
        /ip/route/disable [find where routing-table="VPN-OUT" and (comment="NetX-ovpn" or comment="ZT-Ninja-ovpn")]
        /interface/ovpn-client/enable $ovpn
        /ip/route/enable $ovpnRoute
        /system/script/run [find where name="LAN1-KILLSWITCH-ENABLE"]
    }
    :if ($mode = "blocked") do={
        /ip/route/disable $vlessRoute
        /ip/route/disable [find where routing-table="VPN-OUT" and (comment="NetX-ovpn" or comment="ZT-Ninja-ovpn")]
        /interface/ovpn-client/disable [find]
        :if ([/container/get $tun status] = "running") do={ /container/stop $tun }
        :if ([/container/get $xray status] = "running") do={ /container/stop $xray }
        /system/script/run [find where name="LAN1-KILLSWITCH-ENABLE"]
    }
    :if ($mode = "direct") do={
        /ip/route/disable $vlessRoute
        /ip/route/disable [find where routing-table="VPN-OUT" and (comment="NetX-ovpn" or comment="ZT-Ninja-ovpn")]
        /interface/ovpn-client/disable [find]
        :if ([/container/get $tun status] = "running") do={ /container/stop $tun }
        :if ([/container/get $xray status] = "running") do={ /container/stop $xray }
        /system/script/run [find where name="LAN1-DIRECT-ENABLE"]
    }

    :if ($requested = true) do={
        :local desiredText [:serialize to=json value={"mode"=$mode} options=json.no-string-conversion]
        :if ([:len $desiredFile] = 1) do={ /file/set $desiredFile contents=$desiredText } else={ /file/add name=$desiredPath contents=$desiredText }
        /file/remove $requestFile
        /ip/firewall/connection/remove [find where src-address~"192.168.88."]
    }

    :local applyFile [/file/find where name=$applyPath]
    :if (($mode = "vless") and ([:len $applyFile] = 1)) do={
        :local aq [:deserialize from=json value=[/file/get $applyFile contents] options=json.no-string-conversion]
        :local activeID ($aq->"profile_id")
        /system/script/run [find where name="VLESS-Apply-Coordinator"]
        :global VLESS_ACTIVE_PROFILE $activeID
        /ip/firewall/connection/remove [find where src-address~"192.168.88."]
    }

    :local xr "stopped"; :if ([/container/get $xray status] = "running") do={ :set xr "running" }
    :local tn "stopped"; :if ([/container/get $tun status] = "running") do={ :set tn "running" }
    :local rt "disabled"; :if ([/ip/route/get $vlessRoute disabled] = false) do={ :set rt "enabled" }
    :local op "disabled"; :if ([:len $ovpn] = 1) do={ :if ([/interface/ovpn-client/get $ovpn disabled] = false) do={ :set op $ovpnName } }
    :local st {"mode"=$mode;"desired_mode"=$mode;"active_profile"=$VLESS_ACTIVE_PROFILE;"xray"=$xr;"tun2socks"=$tn;"route"=$rt;"ovpn"=$op}
    :local statusText [:serialize to=json value=$st options=json.no-string-conversion]
    :local statusFile [/file/find where name=$statusPath]
    :if ([:len $statusFile] = 1) do={ /file/set $statusFile contents=$statusText } else={ /file/add name=$statusPath contents=$statusText }
    :log info ($pfx.": mode reconciled: ".$mode)
}

/system/scheduler/remove [find where name="VLESS-Mode-Watcher"]
/system/scheduler/add name="VLESS-Mode-Watcher" interval=10s start-time=startup on-event="/system/script/run VLESS-Mode-Manager" policy=read,write,test,password,sensitive

/system/scheduler/remove [find where name="VLESS-Boot-Recovery"]
/system/scheduler/add name="VLESS-Boot-Recovery" start-time=startup on-event=":delay 45s; /system/script/run VLESS-Mode-Manager; /ip/firewall/connection/remove [find where src-address~\"192.168.88.\"]" policy=read,write,test,password,sensitive
