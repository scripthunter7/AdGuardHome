import React, { Component, Fragment } from 'react';
import { NavLink } from 'react-router-dom';
import PropTypes from 'prop-types';
import enhanceWithClickOutside from 'react-click-outside';
import classnames from 'classnames';

class Menu extends Component {
    handleClickOutside = () => {
        this.props.closeMenu();
    };

    toggleMenu = () => {
        this.props.toggleMenuOpen();
    };

    render() {
        const menuClass = classnames({
            'col-lg mobile-menu': true,
            'mobile-menu--active': this.props.isMenuOpen,
        });

        return (
            <Fragment>
                <div className={menuClass}>
                    <ul className="nav nav-tabs border-0 flex-column flex-lg-row">
                        <li className="nav-item border-bottom d-lg-none" onClick={this.toggleMenu}>
                            <div className="nav-link nav-link--back">
                                <svg className="nav-icon" fill="none" height="24" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" viewBox="0 0 24 24" width="24" xmlns="http://www.w3.org/2000/svg"><path d="m19 12h-14"/><path d="m12 19-7-7 7-7"/></svg>
                                Back
                            </div>
                        </li>
                        <li className="nav-item">
                            <NavLink to="/" exact={true} className="nav-link">
                                <svg className="nav-icon" fill="none" height="24" stroke="#9aa0ac" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" viewBox="0 0 24 24" width="24" xmlns="http://www.w3.org/2000/svg"><path d="m3 9 9-7 9 7v11a2 2 0 0 1 -2 2h-14a2 2 0 0 1 -2-2z"/><path d="m9 22v-10h6v10"/></svg>
                                Dashboard
                            </NavLink>
                        </li>
                        <li className="nav-item">
                            <NavLink to="/settings" className="nav-link">
                                <svg className="nav-icon" fill="none" height="24" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" viewBox="0 0 24 24" width="24" xmlns="http://www.w3.org/2000/svg"><circle cx="12" cy="12" r="3"/><path d="m19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1 -2.83 0l-.06-.06a1.65 1.65 0 0 0 -1.82-.33 1.65 1.65 0 0 0 -1 1.51v.17a2 2 0 0 1 -2 2 2 2 0 0 1 -2-2v-.09a1.65 1.65 0 0 0 -1.08-1.51 1.65 1.65 0 0 0 -1.82.33l-.06.06a2 2 0 0 1 -2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0 -1.51-1h-.17a2 2 0 0 1 -2-2 2 2 0 0 1 2-2h.09a1.65 1.65 0 0 0 1.51-1.08 1.65 1.65 0 0 0 -.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33h.08a1.65 1.65 0 0 0 1-1.51v-.17a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0 -.33 1.82v.08a1.65 1.65 0 0 0 1.51 1h.17a2 2 0 0 1 2 2 2 2 0 0 1 -2 2h-.09a1.65 1.65 0 0 0 -1.51 1z"/></svg>
                                Settings
                            </NavLink>
                        </li>
                        <li className="nav-item">
                            <NavLink to="/filters" className="nav-link">
                                <svg className="nav-icon" fill="none" height="24" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" viewBox="0 0 24 24" width="24" xmlns="http://www.w3.org/2000/svg"><path d="m22 3h-20l8 9.46v6.54l4 2v-8.54z"/></svg>
                                Filters
                            </NavLink>
                        </li>
                        <li className="nav-item">
                            <NavLink to="/logs" className="nav-link">
                                <svg className="nav-icon" fill="none" height="24" strokeLinecap="round" strokeLinejoin="round" strokeWidth="2" viewBox="0 0 24 24" width="24" xmlns="http://www.w3.org/2000/svg"><path d="m14 2h-8a2 2 0 0 0 -2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-12z"/><path d="m14 2v6h6"/><path d="m16 13h-8"/><path d="m16 17h-8"/><path d="m10 9h-1-1"/></svg>
                                Query Log
                            </NavLink>
                        </li>
                    </ul>
                </div>
            </Fragment>
        );
    }
}

Menu.propTypes = {
    isMenuOpen: PropTypes.bool,
    closeMenu: PropTypes.func,
    toggleMenuOpen: PropTypes.func,
};

export default enhanceWithClickOutside(Menu);
