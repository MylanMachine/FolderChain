# Code signing FolderChain.exe

Windows 11's **Smart App Control** refuses to run programs that aren't signed by a verified publisher (there's no "Run anyway"). FolderChain is signed with **Azure Artifact Signing** (formerly Trusted Signing), Microsoft's managed signing service. A signed exe runs everywhere and shows the verified publisher name in Windows prompts.

Setup is a one-time job of roughly an hour of clicking, plus Microsoft's identity check (individuals: minutes, via an ID scan; organizations: 1–20 business days).

## Who can get a certificate

- **Organization** (certificate says your business name, e.g. "Mylan Machine"): a registered business in the US, Canada, EU, UK, Australia, NZ, Japan, South Korea, Singapore, Switzerland, Norway or Israel. Needs a business website, an email address **on that website's domain**, and business registration details.
- **Individual** (certificate says your personal legal name): must live in the US or Canada. The Azure billing account must be type *Individual* and its legal name and address must exactly match your government ID.

Either way you need a **paid** Azure subscription (pay-as-you-go is fine; free/trial subscriptions are rejected). Billing starts when the signing account is created. The Basic tier includes 5,000 signatures/month, far more than needed; check the current price on the [pricing page](https://azure.microsoft.com/pricing/details/artifact-signing/).

## Part 1 — Azure portal (you do this once)

1. **Subscription.** Sign in at <https://portal.azure.com>. If you have no subscription, create a *Pay-As-You-Go* one (Subscriptions → Add).
   *Individuals:* first check Cost Management + Billing → Billing account → Properties: Account type is **Individual**, and the legal name and sold-to address match your ID exactly.
2. **Register the service.** Subscriptions → your subscription → Settings → **Resource providers** → select **Microsoft.CodeSigning** → **Register**.
3. **Create the signing account.** Search **Artifact Signing Accounts** → **Create**:
   - Resource group: *Create new* → `folderchain-signing`
   - Account name: e.g. `mylanmachine-signing` (3–24 letters/numbers/hyphens, globally unique)
   - Region: the closest US region, e.g. **East US** (note which one; it decides the endpoint below)
   - Pricing: **Basic**
   - **Review + Create**, then **Go to resource**.
4. **Give yourself the two roles.** In the signing account → **Access control (IAM)** → **Add role assignment**, add your own user to:
   - **Artifact Signing Identity Verifier** (to request validation)
   - **Artifact Signing Certificate Profile Signer** (to sign)
   Role changes can take a few minutes to apply; sign out and back in if buttons stay greyed out.
5. **Verify your identity.** In the signing account → **Identity validations**:
   - *Organization:* choose **Organization** → **New identity** → **Public**, fill in the legal business name, website, an email on the business domain, and address → **Create**. When the status becomes **Action Required**, follow the emailed link to verify the person named on the request (ID scan on your phone + Microsoft Authenticator).
   - *Individual:* change the dropdown to **Individual** → **New identity** → **Public**, select your billing account (the form fills itself in) → **Create**. When the status becomes **Action Required**, click your name → the verification link, and follow the ID scan + Microsoft Authenticator steps.
   Wait until the status is **Completed**.
6. **Create the certificate profile.** In the signing account → **Certificate profiles** → **Create** → **Public Trust**:
   - Name: e.g. `folderchain`
   - Verified CN and O: pick your completed identity validation
   - **Create**.

## Part 2 — this computer (once)

Run these in PowerShell:

```powershell
winget install -e --id Microsoft.Azure.ArtifactSigningClientTools
winget install -e --id Microsoft.AzureCLI
```

Close and reopen PowerShell, then sign in to Azure (opens a browser):

```powershell
az login
```

Copy `signing\metadata.example.json` to `signing\metadata.json` and fill in:

| Field | Value |
|---|---|
| `Endpoint` | your region's endpoint: East US `https://eus.codesigning.azure.net`, West US 2 `https://wus2.codesigning.azure.net`, Central US `https://cus.codesigning.azure.net`, West US 3 `https://wus3.codesigning.azure.net` ([full list](https://learn.microsoft.com/azure/artifact-signing/how-to-signing-integrations)) |
| `CodeSigningAccountName` | the account name from step 3 |
| `CertificateProfileName` | the profile name from step 6 |

`signing\metadata.json` is git-ignored. It contains no secrets, but it's specific to your account.

## Part 3 — build signed

```powershell
.\build.ps1 -RequireSigning
```

The script builds, signs with a Microsoft timestamp, and verifies the signature. Check it yourself: right-click `dist\FolderChain.exe` → Properties → **Digital Signatures**.

## Troubleshooting

- **403 Forbidden / SignerSign() failed:** the endpoint region doesn't match the account's region, or the account/profile name has a typo, or the *Certificate Profile Signer* role isn't assigned (or hasn't applied yet; wait a few minutes and `az login` again).
- **"Azure.CodeSigning.Dlib.dll not found":** reinstall the client tools, or set `$env:FC_SIGN_DLIB` to the full path of the x64 `Azure.CodeSigning.Dlib.dll`.
- **New identity button greyed out:** you don't have the *Identity Verifier* role yet.

Docs: [quickstart](https://learn.microsoft.com/azure/artifact-signing/quickstart) · [SignTool setup](https://learn.microsoft.com/azure/artifact-signing/how-to-signing-integrations) · [roles](https://learn.microsoft.com/azure/artifact-signing/tutorial-assign-roles)
