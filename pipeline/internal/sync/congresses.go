package sync

import (
	"context"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/rollcall"
)

// MinCurrentMemberTerms is how many member terms a congress needs before PromoteCongress marks
// it current. A congress has 541 seats (435 representatives, 6 delegates, 100 senators), so 500
// allows for vacancies but not for a members sync that has barely started. Until the new
// congress reaches it, find-my-reps keeps answering with the previous one instead of nobody.
const MinCurrentMemberTerms = 500

// EnsureCongress writes the congress's congresses row, which its votes and member terms need
// (fk_cv_congress, fk_member_terms_congress). It starts on January 3 and ends when the next
// congress starts, as seed-congress writes it. It doesn't change which congress is current.
func (s *Service) EnsureCongress(ctx context.Context, congressNum int) error {
	return s.store.UpsertCongress(ctx, repository.CongressRow{
		Number:    congressNum,
		StartDate: rollcall.Start(congressNum),
		EndDate:   rollcall.Start(congressNum + 1),
	})
}

// PromoteCongress marks the congress current, and every other congress not current, once it
// has at least MinCurrentMemberTerms member terms. It reports whether the congress is current
// when it returns.
func (s *Service) PromoteCongress(ctx context.Context, congressNum int) (bool, error) {
	terms, err := s.store.CountMemberTerms(ctx, congressNum)
	if err != nil {
		return false, err
	}
	if terms < MinCurrentMemberTerms {
		s.logger.InfoContext(ctx, "congress not marked current yet: too few member terms",
			"congress", congressNum, "member_terms", terms, "needed", MinCurrentMemberTerms)
		return false, nil
	}
	changed, err := s.store.SetCurrentCongress(ctx, congressNum)
	if err != nil {
		return false, err
	}
	if changed {
		s.logger.InfoContext(ctx, "marked congress current", "congress", congressNum, "member_terms", terms)
	}
	return true, nil
}
