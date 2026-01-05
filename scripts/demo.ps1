param(
  [int]$BasePort = 8081
)

$ErrorActionPreference = 'Stop'

Write-Host "Starting 3 nodes..."

$node1 = Start-Process -PassThru -WindowStyle Normal -FilePath "go" -ArgumentList @("run", "./cmd/godist", "-id", "node-1", "-addr", "127.0.0.1:$BasePort", "-N", "3", "-R", "2", "-W", "2")
Start-Sleep -Milliseconds 300

$node2 = Start-Process -PassThru -WindowStyle Normal -FilePath "go" -ArgumentList @("run", "./cmd/godist", "-id", "node-2", "-addr", "127.0.0.1:" + ($BasePort+1), "-seed", "127.0.0.1:$BasePort", "-N", "3", "-R", "2", "-W", "2")
Start-Sleep -Milliseconds 300

$node3 = Start-Process -PassThru -WindowStyle Normal -FilePath "go" -ArgumentList @("run", "./cmd/godist", "-id", "node-3", "-addr", "127.0.0.1:" + ($BasePort+2), "-seed", "127.0.0.1:$BasePort", "-N", "3", "-R", "2", "-W", "2")
Start-Sleep -Seconds 2

Write-Host "Put key=alpha via node-1"
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:$BasePort/kv/put" -ContentType "application/json" -Body '{"key":"alpha","value":"first"}' | ConvertTo-Json

Write-Host "Get key=alpha via node-2"
Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:" + ($BasePort+1) + "/kv/get/alpha" | ConvertTo-Json

Write-Host "Killing node-2 (simulated failure)"
Stop-Process -Id $node2.Id -Force
Start-Sleep -Seconds 3

Write-Host "Put key=alpha via node-3 (should still succeed with W=2 out of N=3)"
Invoke-RestMethod -Method Post -Uri "http://127.0.0.1:" + ($BasePort+2) + "/kv/put" -ContentType "application/json" -Body '{"key":"alpha","value":"second"}' | ConvertTo-Json

Write-Host "Get key=alpha via node-1"
Invoke-RestMethod -Method Get -Uri "http://127.0.0.1:$BasePort/kv/get/alpha" | ConvertTo-Json

Write-Host "Done. Stop remaining nodes with Ctrl+C in their windows, or:"
Write-Host "Stop-Process -Id $($node1.Id), $($node3.Id) -Force"
