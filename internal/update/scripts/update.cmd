@echo off
rem sshore 升级脚本：由主程序按本次升级写出并用环境变量传参，不要手工修改。
rem 变量：SSHORE_PID/SSHORE_TARGET/SSHORE_PENDING/SSHORE_BACKUP/SSHORE_SIZE/SSHORE_LOG/SSHORE_WAIT
rem 日志协议：每步失败写 STEP=<n> ERR=<原因>，末行 RESULT=ok 或 RESULT=fail:<step>
setlocal enabledelayedexpansion
set "PID=%SSHORE_PID%"
set "TARGET=%SSHORE_TARGET%"
set "PENDING=%SSHORE_PENDING%"
set "BACKUP=%SSHORE_BACKUP%"
set "SIZE=%SSHORE_SIZE%"
set "LOG=%SSHORE_LOG%"
set "WAIT=%SSHORE_WAIT%"
if "%WAIT%"=="" set "WAIT=60"
if "%LOG%"=="" set "LOG=%TEMP%\sshore-update.log"
type nul > "%LOG%" 2>nul
if not exist "%LOG%" (set "ARGSERR=无法创建日志文件" & goto :args)

if "%PID%"=="" (set "ARGSERR=PID 缺失" & goto :args)
if "%SIZE%"=="" (set "ARGSERR=SIZE 缺失" & goto :args)
if "%WAIT%"=="" (set "ARGSERR=WAIT 缺失" & goto :args)
rem PID/SIZE/WAIT 必须是正整数（spec §8.2）：拼起来的字符串只含数字才算通过
for %%A in (%PID%) do if "%%A"=="" (set "ARGSERR=PID 非法" & goto :args)
echo %PID%%SIZE%%WAIT%| findstr /r "^[0-9][0-9]*$" >nul || (set "ARGSERR=PID/SIZE/WAIT 非法" & goto :args)
if "%TARGET%"=="" (set "ARGSERR=TARGET 缺失" & goto :args)
if "%PENDING%"=="" (set "ARGSERR=PENDING 缺失" & goto :args)
if not exist "%TARGET%" (set "ARGSERR=TARGET 不存在" & goto :args)
if not exist "%PENDING%" (set "ARGSERR=PENDING 不存在" & goto :args)
rem BACKUP/LOG 本次运行才创建，只要求父目录存在且可写（spec §8.2）
for %%A in ("%BACKUP%") do if not exist "%%~dpA" (set "ARGSERR=BACKUP 父目录不存在" & goto :args)
for %%A in ("%LOG%") do if not exist "%%~dpA" (set "ARGSERR=LOG 父目录不存在" & goto :args)
rem BACKUP 必须是文件路径：指向已存在目录时 move 会把它当成目标目录，正式名会消失
if exist "%BACKUP%\" (set "ARGSERR=BACKUP 指向已存在目录" & goto :args)

cd /d "%~dp0" || goto :step0
for %%A in ("%PENDING%") do set "PB=%%~nxA"
for %%A in ("%BACKUP%") do set "BB=%%~nxA"

rem 2) 等旧进程退出
set /a WAITED=0
:wait
tasklist /FI "PID eq %PID%" 2>nul | find "%PID%" >nul
if errorlevel 1 goto :waited
if %WAITED% GEQ %WAIT% goto :waitfail
ping -n 2 127.0.0.1 >nul
set /a WAITED+=1
goto :wait
:waited

rem 3) 自检大小（必须用延迟展开读在同一块里刚 set 的变量）
for %%A in ("%PENDING%") do set "PEND_SIZE=%%~zA"
if not "!PEND_SIZE!"=="%SIZE%" goto :step3

rem 4) 清理更早备份（保留 pending 与本次 backup，且跳过 sidecar/脚本/日志）
for %%f in ("%CD%\sshore.v*") do call :maybe_del "%%~nxf"
for %%f in ("%CD%\sshore.dev-*") do call :maybe_del "%%~nxf"

rem 5) 备份旧二进制；改完立刻确认备份确实就位（spec §8.3：任何一步失败都不得让应用消失）
move /y "%TARGET%" "%BACKUP%" >nul || goto :rollback_step5
if not exist "%BACKUP%" goto :rollback_step5
rem 6) 换成新版本（失败则回滚）
if not exist "%PENDING%" goto :rollback_step3
move /y "%PENDING%" "%TARGET%" >nul || goto :rollback_replace

rem 8) 启动并做 3 秒存活探测
start "" "%TARGET%"
ping -n 4 127.0.0.1 >nul
for %%A in ("%TARGET%") do set "EXENAME=%%~nxA"
tasklist /FI "IMAGENAME eq !EXENAME!" 2>nul | find /i "!EXENAME!" >nul
if errorlevel 1 goto :rollback_launch

rem 9) 成功
>> "%LOG%" echo RESULT=ok
del "%LOG%" >nul 2>nul
rem 自删必须用 (goto) 惯用法：cmd.exe 一旦发现批处理删掉了自己就会立即中止，
rem 后面的 exit /b 0 根本不会执行（Task 19 真机实测退 1）；(goto) 让本行先被完整解析。
(goto) 2>nul & del "%~f0" >nul 2>nul & exit /b 0

:maybe_del
set "N=%~1"
if /i "%N%"=="%PB%" exit /b 0
if /i "%N%"=="%BB%" exit /b 0
if /i "%N:~-7%"==".sha256" exit /b 0
if /i "%N%"=="sshore-update.cmd" exit /b 0
if /i "%N%"=="sshore-update.log" exit /b 0
del "%CD%\%N%" >nul 2>nul
exit /b 0

:rollback_replace
call :rollback_target
if not exist "%TARGET%" goto :fail6
goto :fail6

:rollback_step5
rem 第 5 步之后 TARGET 可能已被改名到 BACKUP：失败必须先恢复正式名，避免应用消失
call :rollback_target
goto :step5

:rollback_step3
call :rollback_target
goto :step3

:rollback_target
move /y "%BACKUP%" "%TARGET%" >nul 2>nul
exit /b 0

:rollback_launch
move /y "%TARGET%" "%PENDING%" >nul 2>nul
move /y "%BACKUP%" "%TARGET%" >nul 2>nul
start "" "%TARGET%"
>> "%LOG%" echo STEP=launch ERR=新版本启动失败，已回滚到旧版本
>> "%LOG%" echo RESULT=fail:launch
exit /b 3

:args
if not defined ARGSERR set "ARGSERR=参数非法"
>> "%LOG%" echo STEP=args ERR=!ARGSERR!
>> "%LOG%" echo RESULT=fail:args
exit /b 2
:step0
>> "%LOG%" echo STEP=0 ERR=无法进入目标目录
>> "%LOG%" echo RESULT=fail:0
exit /b 3
:waitfail
>> "%LOG%" echo STEP=wait ERR=等待旧进程退出超时
>> "%LOG%" echo RESULT=fail:wait
exit /b 3
:step3
>> "%LOG%" echo STEP=3 ERR=pending 不存在或大小不符
>> "%LOG%" echo RESULT=fail:3
exit /b 3
:step5
>> "%LOG%" echo STEP=5 ERR=备份旧二进制失败
>> "%LOG%" echo RESULT=fail:5
exit /b 3
:fail6
>> "%LOG%" echo STEP=6 ERR=替换正式名失败
>> "%LOG%" echo RESULT=fail:6
exit /b 3
