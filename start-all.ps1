# start-all.ps1
# Starts the OCPI server, the Mock CPO and the Mock eMSP, each in its own window.

# Run from the project folder, wherever this script is.
Set-Location $PSScriptRoot

Write-Host "Starting OCPI server (port 8080)..."
Start-Process powershell -ArgumentList "-NoExit", "-Command", "go run .\cmd\ocpi-server"
Start-Sleep -Seconds 2

Write-Host "Starting Mock CPO (port 8081)..."
Start-Process powershell -ArgumentList "-NoExit", "-Command", "go run .\cmd\mock-cpo"
Start-Sleep -Seconds 2

Write-Host "Starting Mock eMSP (port 8083)..."
Start-Process powershell -ArgumentList "-NoExit", "-Command", "go run .\cmd\mock-emsp"

Write-Host "All three started. Check each new window for errors."
