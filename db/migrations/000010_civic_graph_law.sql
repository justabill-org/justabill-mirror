-- Law in the ontology (docs/design/149-law-aware-assistant.md, item 1): US Code sections become
-- LawSection nodes, and bill_law_refs becomes CHANGES_LAW edges from a bill to the sections its
-- text versions reference. The graph is redefined whole; everything else is unchanged from
-- 000001_baseline.sql. A "nonusc:" ref has no LawSection row, so its edge doesn't match, like
-- any other edge whose far end isn't synced.
CREATE OR REPLACE PROPERTY GRAPH civic_graph
  NODE TABLES (
    bills KEY (bill_id) LABEL Bill
      PROPERTIES (bill_id, congress, bill_type, number, title, introduced_date, origin_chamber,
        current_status, status_date, policy_area_id),
    members KEY (bioguide_id) LABEL Member
      PROPERTIES (bioguide_id, first_name, last_name),
    congresses KEY (number) LABEL Congress
      PROPERTIES (number, start_date, end_date, is_current),
    committees KEY (committee_id) LABEL Committee
      PROPERTIES (committee_id, name, chamber, committee_type),
    subjects KEY (subject_id) LABEL Subject
      PROPERTIES (subject_id, name),
    policy_areas KEY (policy_area_id) LABEL PolicyArea
      PROPERTIES (policy_area_id, name),
    congressional_votes KEY (vote_id) LABEL RollCall
      PROPERTIES (vote_id, congress, chamber, session, roll_number, vote_date, question, result,
        yeas, nays, present, not_voting),
    amendments KEY (bill_id, amendment_id) LABEL Amendment
      PROPERTIES (amendment_id, congress, amendment_type, amendment_number, description, purpose,
        submitted_date, chamber),
    gao_reports KEY (report_id) LABEL GaoReport
      PROPERTIES (report_id, title, report_number, report_type, published_date),
    usc_sections KEY (section_id) LABEL LawSection
      PROPERTIES (section_id, title_number, section_number, heading, status, positive_law)
  )
  EDGE TABLES (
    bill_sponsorships KEY (bill_id, member_id)
      SOURCE KEY (member_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (bill_id) REFERENCES bills (bill_id)
      LABEL SPONSORED PROPERTIES (role, sponsored_date, is_original),
    member_votes AS cast_votes KEY (vote_id, member_id)
      SOURCE KEY (member_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (vote_id) REFERENCES congressional_votes (vote_id)
      LABEL CAST_VOTE PROPERTIES (vote),
    congressional_votes AS roll_call_on KEY (vote_id)
      SOURCE KEY (vote_id) REFERENCES congressional_votes (vote_id)
      DESTINATION KEY (bill_id) REFERENCES bills (bill_id)
      LABEL ON_BILL NO PROPERTIES,
    bill_relations KEY (bill_id, related_bill_id, relation_type)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (related_bill_id) REFERENCES bills (bill_id)
      LABEL RELATED_TO PROPERTIES (relation_type, identified_by),
    bill_committees KEY (bill_id, committee_id, activity)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (committee_id) REFERENCES committees (committee_id)
      LABEL REFERRED_TO PROPERTIES (activity, activity_date),
    bill_subjects KEY (bill_id, subject_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (subject_id) REFERENCES subjects (subject_id)
      LABEL ABOUT NO PROPERTIES,
    bills AS in_policy_area KEY (bill_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (policy_area_id) REFERENCES policy_areas (policy_area_id)
      LABEL IN_POLICY_AREA NO PROPERTIES,
    bills AS introduced_in KEY (bill_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (congress) REFERENCES congresses (number)
      LABEL INTRODUCED_IN NO PROPERTIES,
    amendments AS amends KEY (bill_id, amendment_id)
      SOURCE KEY (bill_id, amendment_id) REFERENCES amendments (bill_id, amendment_id)
      DESTINATION KEY (bill_id) REFERENCES bills (bill_id)
      LABEL AMENDS NO PROPERTIES,
    amendments AS sponsored_amendment KEY (bill_id, amendment_id)
      SOURCE KEY (sponsor_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (bill_id, amendment_id) REFERENCES amendments (bill_id, amendment_id)
      LABEL SPONSORED_AMENDMENT NO PROPERTIES,
    member_terms AS served_in KEY (member_id, congress, chamber)
      SOURCE KEY (member_id) REFERENCES members (bioguide_id)
      DESTINATION KEY (congress) REFERENCES congresses (number)
      LABEL SERVED_IN PROPERTIES (chamber, state, district, party, start_date, end_date),
    bill_gao_reports AS cites KEY (bill_id, report_id)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (report_id) REFERENCES gao_reports (report_id)
      LABEL CITES PROPERTIES (linked_at),
    bill_law_refs AS changes_law KEY (bill_id, version_id, section_id, ref_kind)
      SOURCE KEY (bill_id) REFERENCES bills (bill_id)
      DESTINATION KEY (section_id) REFERENCES usc_sections (section_id)
      LABEL CHANGES_LAW PROPERTIES (ref_kind, subsection_path, version_id)
  );
