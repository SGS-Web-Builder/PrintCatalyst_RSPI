-- Switch the original, unchanged default off. Preserve customized field policies.
UPDATE portal_customer_details
SET settings=json_set(settings,'$.enabled',json('false'))
WHERE settings='{"enabled":true,"fields":{"customerName":"required","customerPhone":"required","customerEmail":"optional","customerNotes":"optional"}}';
