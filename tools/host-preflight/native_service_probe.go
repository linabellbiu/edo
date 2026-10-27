package main

// 进程可能通过 proctitle 隐藏启动参数；只输出固定形态布尔，不回显原始命令行。
const nativeServiceProbe = `
printf 'HOST_K3S_SERVICE_IDENTITY\n'
edo_service_active=0
if systemctl is-active --quiet k3s.service 2>/dev/null;then edo_service_active=1;fi
edo_main_pid=$(systemctl show k3s.service --property=ExecMainPID --value 2>/dev/null) || edo_main_pid=
edo_pid_valid=0
case "$edo_main_pid" in ''|0|*[!0-9]*)edo_main_pid=0;;*)if [ "${#edo_main_pid}" -le 10 ];then edo_pid_valid=1;else edo_main_pid=0;fi;;esac
edo_process_exists=0
edo_exe_expected=0
edo_cmdline_readable=0
edo_original_argv=0
edo_title_absolute=0
edo_title_basename=0
if [ "$edo_pid_valid" = 1 ] && [ -d "/proc/$edo_main_pid" ];then
  edo_process_exists=1
  edo_exe=$(readlink -f "/proc/$edo_main_pid/exe" 2>/dev/null) || edo_exe=
  if [ "$edo_exe" = /usr/local/bin/k3s ];then edo_exe_expected=1;fi
  if [ -r "/proc/$edo_main_pid/cmdline" ] && command -v head >/dev/null 2>&1 && command -v tr >/dev/null 2>&1;then
    edo_cmdline_readable=1
    edo_cmdline=$(head -c 65536 "/proc/$edo_main_pid/cmdline" 2>/dev/null | tr '\000' '\n') || edo_cmdline=
    if [ "$edo_cmdline" = "$(printf '/usr/local/bin/k3s\nserver\n--config\n/etc/rancher/k3s/config.yaml')" ];then edo_original_argv=1;fi
    if [ "$edo_cmdline" = '/usr/local/bin/k3s server' ];then edo_title_absolute=1;fi
    if [ "$edo_cmdline" = 'k3s server' ];then edo_title_basename=1;fi
  fi
fi
edo_identity_stage=matched
if [ "$edo_service_active" != 1 ];then edo_identity_stage=inactive
elif [ "$edo_pid_valid" != 1 ];then edo_identity_stage=invalid_pid
elif [ "$edo_process_exists" != 1 ];then edo_identity_stage=missing_process
elif [ "$edo_exe_expected" != 1 ];then edo_identity_stage=unexpected_executable
elif [ "$edo_cmdline_readable" != 1 ];then edo_identity_stage=unreadable_arguments
elif [ "$edo_original_argv" != 1 ];then edo_identity_stage=unexpected_arguments
fi
printf 'K3S_IDENTITY_STAGE=%s MainPID=%s Active=%s PIDValid=%s ProcessExists=%s ExeExpected=%s CmdlineReadable=%s OriginalArgv=%s TitleAbsolute=%s TitleBasename=%s\n' "$edo_identity_stage" "$edo_main_pid" "$edo_service_active" "$edo_pid_valid" "$edo_process_exists" "$edo_exe_expected" "$edo_cmdline_readable" "$edo_original_argv" "$edo_title_absolute" "$edo_title_basename"
unset edo_cmdline edo_exe
`
