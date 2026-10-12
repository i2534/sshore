@echo off
rem sshore upgrade script: written by the main program for this upgrade; do not edit by hand.
rem vars: SSHORE_PID/SSHORE_TARGET/SSHORE_PENDING/SSHORE_BACKUP/SSHORE_SIZE/SSHORE_LOG/SSHORE_WAIT
rem log protocol: each failure writes STEP=<n> ERR=<reason>; last line RESULT=ok or RESULT=fail:<step>
rem ASCII ONLY. cmd.exe decodes this file with the console code page; non-ASCII bytes (e.g. a
rem Chinese comment) can swallow the line terminator and shift every following line, making the
rem script abort within milliseconds with EXIT=255 and an empty log (measured on real Windows).
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
if not exist "%LOG%" (set "ARGSERR=cannot create log file" & goto :args)

if "%PID%"=="" (set "ARGSERR=PID missing" & goto :args)
if "%SIZE%"=="" (set "ARGSERR=SIZE missing" & goto :args)
if "%WAIT%"=="" (set "ARGSERR=WAIT missing" & goto :args)
rem PID/SIZE/WAIT must be positive integers (spec 8.2): only the concatenation may contain digits
for %%A in (%PID%) do if "%%A"=="" (set "ARGSERR=PID invalid" & goto :args)
echo %PID%%SIZE%%WAIT%| findstr /r "^[0-9][0-9]*$" >nul || (set "ARGSERR=PID/SIZE/WAIT invalid" & goto :args)
if "%TARGET%"=="" (set "ARGSERR=TARGET missing" & goto :args)
if "%PENDING%"=="" (set "ARGSERR=PENDING missing" & goto :args)
if not exist "%TARGET%" (set "ARGSERR=TARGET not found" & goto :args)
if not exist "%PENDING%" (set "ARGSERR=PENDING not found" & goto :args)
rem BACKUP/LOG are created on this run: only their parent dir must exist and be writable (spec 8.2)
for %%A in ("%BACKUP%") do if not exist "%%~dpA" (set "ARGSERR=BACKUP parent dir missing" & goto :args)
for %%A in ("%LOG%") do if not exist "%%~dpA" (set "ARGSERR=LOG parent dir missing" & goto :args)
rem BACKUP must be a file path: pointing at an existing dir makes move treat it as the destination
if exist "%BACKUP%\" (set "ARGSERR=BACKUP points to an existing directory" & goto :args)

cd /d "%~dp0" || goto :step0
for %%A in ("%PENDING%") do set "PB=%%~nxA"
for %%A in ("%BACKUP%") do set "BB=%%~nxA"

rem 2) wait for the old process to exit
set /a WAITED=0
:wait
tasklist /FI "PID eq %PID%" 2>nul | find "%PID%" >nul
if errorlevel 1 goto :waited
if %WAITED% GEQ %WAIT% goto :waitfail
ping -n 2 127.0.0.1 >nul
set /a WAITED+=1
goto :wait
:waited

rem 3) verify size (delayed expansion: PEND_SIZE is set in the line right above)
for %%A in ("%PENDING%") do set "PEND_SIZE=%%~zA"
if not "!PEND_SIZE!"=="%SIZE%" goto :step3

rem 4) drop older backups (keep this pending and this backup; skip sidecar/script/log)
for %%f in ("%CD%\sshore.v*") do call :maybe_del "%%~nxf"
for %%f in ("%CD%\sshore.dev-*") do call :maybe_del "%%~nxf"

rem 5) back up the old binary, then confirm the backup is in place (spec 8.3: never lose the app)
move /y "%TARGET%" "%BACKUP%" >nul || goto :rollback_step5
if not exist "%BACKUP%" goto :rollback_step5
rem 6) install the new version (roll back on failure)
if not exist "%PENDING%" goto :rollback_step3
move /y "%PENDING%" "%TARGET%" >nul || goto :rollback_replace

rem 8) start it and probe liveness for 3 seconds
start "" "%TARGET%"
ping -n 4 127.0.0.1 >nul
for %%A in ("%TARGET%") do set "EXENAME=%%~nxA"
tasklist /FI "IMAGENAME eq !EXENAME!" 2>nul | find /i "!EXENAME!" >nul
if errorlevel 1 goto :rollback_launch

rem 9) success
>> "%LOG%" echo RESULT=ok
del "%LOG%" >nul 2>nul
rem self-delete must use the (goto) idiom: once cmd.exe sees the batch file delete itself it aborts
rem immediately, so a trailing exit /b 0 would never run (measured: exit 1 without it).
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
rem after step 5 TARGET may already be renamed to BACKUP: restore the real name first
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
>> "%LOG%" echo STEP=launch ERR=new version failed to start; rolled back to the old one
>> "%LOG%" echo RESULT=fail:launch
exit /b 3

:args
if not defined ARGSERR set "ARGSERR=invalid arguments"
>> "%LOG%" echo STEP=args ERR=!ARGSERR!
>> "%LOG%" echo RESULT=fail:args
exit /b 2
:step0
>> "%LOG%" echo STEP=0 ERR=cannot enter the target directory
>> "%LOG%" echo RESULT=fail:0
exit /b 3
:waitfail
>> "%LOG%" echo STEP=wait ERR=timed out waiting for the old process to exit
>> "%LOG%" echo RESULT=fail:wait
exit /b 3
:step3
>> "%LOG%" echo STEP=3 ERR=pending missing or size mismatch
>> "%LOG%" echo RESULT=fail:3
exit /b 3
:step5
>> "%LOG%" echo STEP=5 ERR=backup of the old binary failed
>> "%LOG%" echo RESULT=fail:5
exit /b 3
:fail6
>> "%LOG%" echo STEP=6 ERR=replace of the target failed
>> "%LOG%" echo RESULT=fail:6
exit /b 3
