2. The Today decision queue has 3 cards without advice: read the cards with gofer_today_list (unadvised=true) and advise each one with gofer_today_advise (text: ≤60 characters, one line, say why).
   - review (needs review): read the report, the diffstat, the commits and the verify result with gofer_today_card; write a digest of 3–5 lines (change groups / risks / tests) and give action_id accept (pass) or rerun (send it back);
   - suggestion / merge: give adopt or dismiss; interaction: give answer:<value> only when the options are clear, otherwise just write text;
   - decision: add background only, **never an action_id and never the choice**. Your advice is only advice: you perform no action; the user clicks "apply advice".
