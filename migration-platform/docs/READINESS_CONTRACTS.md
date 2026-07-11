# Readiness e contratti read-only

## Scopo

Il readiness report descrive se esistono evidenze sufficienti per progettare un
writer reale. Non abilita writer, non accoda actor e non autorizza scritture.
Ogni report è legato a `migration_id`, `plan_id`, `comparison_report_id`,
`source_snapshot_id` e `destination_snapshot_id` esatti.

## Stati

- `not_ready`: manca un requisito di sicurezza o un contratto fondamentale.
- `needs_inventory`: almeno un dato necessario non è leggibile/verificabile.
- `needs_contract_test`: inventario completo, contratto writer ancora da provare.
- `needs_operator_input`: serviranno password, approval o decisioni umane.
- `eligible_for_real_design`: evidenze read-only sufficienti per progettare il
  writer; non significa writer disponibile, abilitato o verificato.

La priorità conservativa impedisce a un requisito meno grave di nascondere un
blocker più forte. La generazione rifiuta piani, comparazioni o snapshot obsoleti.

## Pipeline delle evidenze

```text
preflight read-only
  -> snapshot sorgente/destinazione immutabili
  -> comparazione legata agli snapshot
  -> piano (evidenze tecniche sempre excluded)
  -> readiness report persistente
```

Una conferma operatore non crea evidenza e non produce mai `verified`.

## Contratti implementati

### Database MySQL

`database_contract` combina:

- `Mysql::get_restrictions`;
- `maximum_databases` da `Variables::get_user_information`;
- conteggio corrente da `Mysql::list_databases`.

La coverage è `succeeded` solo con restrizioni non vuote e quota nota. Il
readiness richiede successo sia su sorgente sia su destinazione.

### Utenti e grant MySQL

`mysql_grants` interroga `Mysql::get_privileges_on_database` per ogni prodotto
utente×database. Conserva soltanto database, utente e privilegi normalizzati.
Fallimenti parziali non diventano una matrice vuota.

`mysql_grant_contract` richiede:

- `pairs_checked == pairs_total`;
- nessun fallimento di lettura;
- privilegi appartenenti all'insieme account-level supportato.

`mysql_grants` e `mysql_grant_contract` sono evidenze, quindi il planner li
marca `excluded` e non crea operazioni duplicate. Le categorie evidence
`excluded` non entrano inoltre nel conteggio readiness delle categorie writer.

### FTP

Gli account migrabili devono avere quota e home directory esplicite da
`Ftp::list_ftp_with_disk`. `ftp_contract` valida il mapping read-only
`login→user/domain/quota/homedir` e richiede il limite
`maximum_ftp_accounts` leggibile. Il readiness richiede successo su entrambi gli
endpoint prima di dichiarare la categoria `eligible_for_real_design`.

### Mailing list

`private` è accettato solo se esplicito. La normalizzazione usa, in ordine:

1. `private` o `listtype` dalla risposta UAPI;
2. campi `archive_private`, `advertised`, `subscribe_policy`;
3. fallback read-only API 2 `Email::listlists` con gli stessi campi.

La provenienza è registrata in `_privacy_source`. Nessun valore implicito viene
inventato; in assenza di evidenza la coverage resta `partial`.
`mailing_list_contract` valida il mapping read-only
`address→list/domain/private` e richiede il limite `maximum_mailing_lists`
leggibile su entrambi gli endpoint.

### Forwarder

`forwarder_contract` normalizza soltanto la coppia completa
`source→destination` restituita da `Email::list_forwarders`. Una coverage
riuscita dimostra che il futuro writer potrà ripetere la stessa lettura subito
prima della scrittura e distinguere una coppia identica da una destinazione
diversa per lo stesso alias. Il readiness richiede successo su entrambi i lati.

### Autoresponder

`autoresponder_contract` richiede lista per dominio, dettaglio riuscito per ogni
indirizzo e presenza dei campi obbligatori del futuro writer. L'evidenza salva
solo indirizzo, nomi dei campi presenti e strategia di fresh read; non copia
body, subject o from. Il percorso reale dovrà rieseguire lista+dettaglio subito
prima dell'upsert e bloccare qualsiasi collisione differente.

### DNS

`DNS::parse_zone` interroga soltanto dominio principale, addon e alias. I
sottodomini sono record della zona genitore e non zone autonome.

`dns_contract` rende esplicite le zone proprietarie attese, la strategia
`parse_zone_per_owned_zone`, le identità record ambigue e i tipi fuori dal
contratto additivo. Il planner conserva separatamente `comparison_state`: un
passo è candidabile soltanto quando è `missing_on_destination`, non ambiguo e di
tipo supportato. `different` e `unknown` restano `not_ready` e manuali; una
conferma forte non li trasforma in scritture sicure.

## Dati esclusi

Readiness e contract evidence non devono contenere token, password, ciphertext,
chiavi Fernet, `auth_secret`, chiavi SSL private o body/subject/from degli
autoresponder. Password e contenuti sensibili non devono apparire neppure nei
messaggi di errore o negli eventi aggregati.

## Stato pilota verificato

- job preflight `17`;
- snapshot `33/34`;
- comparazione `18`;
- piano `12`;
- readiness `9`;
- `needs_inventory=0`;
- database e utenti MySQL `eligible_for_real_design`;
- FTP e mailing list `needs_contract_test`;
- DNS `not_ready` per collisioni/fresh verification;
- tutti i writer e `MOCK_ORCHESTRATOR_MODE` disabilitati.

Gli ID e gli stati sono storici: i nuovi contract richiedono un nuovo preflight,
comparazione, piano e readiness prima di aggiornare la fotografia pilota.

## Contratti pianificati

I contract read-only pianificati sono completi. Non costituiscono un execution
contract reale e non autorizzano l'abilitazione dei writer.

Ogni futuro percorso reale richiederà autorizzazione esplicita separata,
destinazione-only, conferma forte, nessun overwrite/delete implicito e nuovo
preflight/comparazione post-write.
